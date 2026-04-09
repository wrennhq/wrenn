// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk
//
// Modifications by Omukk (Wrenn Sandbox): replaced errgroup with WaitGroup
// + semaphore, replaced fdexit abstraction with pipe, integrated with
// snapshot.Header-based DiffFileSource instead of block.ReadonlyDevice,
// fixed EAGAIN handling in poll loop.

package uffd

/*
#include <linux/userfaultfd.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"

	"git.omukk.dev/wrenn/wrenn/internal/snapshot"
)

const (
	fdSize              = 4
	regionMappingsSize  = 1024
	maxConcurrentFaults = 4096
)

// MemorySource provides page data for the UFFD handler.
// Given a logical memory offset and a size, it returns the page data.
type MemorySource interface {
	ReadPage(ctx context.Context, offset int64, size int64) ([]byte, error)
}

// Server manages the UFFD Unix socket lifecycle and page fault handling
// for a single Firecracker snapshot restore.
type Server struct {
	socketPath string
	source     MemorySource
	lis        *net.UnixListener

	readyCh   chan struct{}
	readyOnce sync.Once
	doneCh    chan struct{}
	doneErr   error

	// exitPipe signals the poll loop to stop.
	exitR *os.File
	exitW *os.File
}

// NewServer creates a UFFD server that will listen on the given socket path
// and serve memory pages from the given source.
func NewServer(socketPath string, source MemorySource) *Server {
	return &Server{
		socketPath: socketPath,
		source:     source,
		readyCh:    make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Start begins listening on the Unix socket. Firecracker will connect to this
// socket after loadSnapshot is called with the UFFD backend.
// Start returns immediately; the server runs in a background goroutine.
func (s *Server) Start(ctx context.Context) error {
	lis, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on uffd socket: %w", err)
	}
	s.lis = lis

	if err := os.Chmod(s.socketPath, 0o777); err != nil {
		lis.Close()
		return fmt.Errorf("chmod uffd socket: %w", err)
	}

	// Create exit signal pipe.
	r, w, err := os.Pipe()
	if err != nil {
		lis.Close()
		return fmt.Errorf("create exit pipe: %w", err)
	}
	s.exitR = r
	s.exitW = w

	go func() {
		defer close(s.doneCh)
		s.doneErr = s.handle(ctx)
		s.lis.Close()
		s.exitR.Close()
		s.exitW.Close()
		s.readyOnce.Do(func() { close(s.readyCh) })
	}()

	return nil
}

// Ready returns a channel that is closed when the UFFD handler is ready
// (after Firecracker has connected and sent the uffd fd).
func (s *Server) Ready() <-chan struct{} {
	return s.readyCh
}

// Stop signals the UFFD poll loop to exit and waits for it to finish.
func (s *Server) Stop() error {
	// Write a byte to the exit pipe to wake the poll loop.
	_, _ = s.exitW.Write([]byte{0})
	<-s.doneCh
	return s.doneErr
}

// Wait blocks until the server exits.
func (s *Server) Wait() error {
	<-s.doneCh
	return s.doneErr
}

// handle accepts the Firecracker connection, receives the UFFD fd via
// SCM_RIGHTS, and runs the page fault poll loop.
func (s *Server) handle(ctx context.Context) error {
	conn, err := s.lis.Accept()
	if err != nil {
		return fmt.Errorf("accept uffd connection: %w", err)
	}

	unixConn := conn.(*net.UnixConn)
	defer unixConn.Close()

	// Read the memory region mappings (JSON) and the UFFD fd (SCM_RIGHTS).
	regionBuf := make([]byte, regionMappingsSize)
	uffdBuf := make([]byte, syscall.CmsgSpace(fdSize))

	nRegion, nFd, _, _, err := unixConn.ReadMsgUnix(regionBuf, uffdBuf)
	if err != nil {
		return fmt.Errorf("read uffd message: %w", err)
	}

	var regions []Region
	if err := json.Unmarshal(regionBuf[:nRegion], &regions); err != nil {
		return fmt.Errorf("parse memory regions: %w", err)
	}

	controlMsgs, err := syscall.ParseSocketControlMessage(uffdBuf[:nFd])
	if err != nil {
		return fmt.Errorf("parse control messages: %w", err)
	}
	if len(controlMsgs) != 1 {
		return fmt.Errorf("expected 1 control message, got %d", len(controlMsgs))
	}

	fds, err := syscall.ParseUnixRights(&controlMsgs[0])
	if err != nil {
		return fmt.Errorf("parse unix rights: %w", err)
	}
	if len(fds) != 1 {
		return fmt.Errorf("expected 1 fd, got %d", len(fds))
	}

	uffdFd := fd(fds[0])
	defer uffdFd.close()

	mapping := NewMapping(regions)

	slog.Info("uffd handler connected",
		"regions", len(regions),
		"fd", int(uffdFd),
	)

	// Signal readiness.
	s.readyOnce.Do(func() { close(s.readyCh) })

	// Run the poll loop.
	return s.serve(ctx, uffdFd, mapping)
}

// serve is the main poll loop. It polls the UFFD fd for page fault events
// and the exit pipe for shutdown signals.
func (s *Server) serve(ctx context.Context, uffdFd fd, mapping *Mapping) error {
	pollFds := []unix.PollFd{
		{Fd: int32(uffdFd), Events: unix.POLLIN},
		{Fd: int32(s.exitR.Fd()), Events: unix.POLLIN},
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentFaults)

	// Always wait for in-flight goroutines before returning, so the caller
	// can safely close the uffd fd after serve returns.
	defer wg.Wait()

	for {
		if _, err := unix.Poll(pollFds, -1); err != nil {
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			return fmt.Errorf("poll: %w", err)
		}

		// Check exit signal.
		if pollFds[1].Revents&unix.POLLIN != 0 {
			return nil
		}

		if pollFds[0].Revents&unix.POLLIN == 0 {
			continue
		}

		// Read the uffd_msg. The fd is O_NONBLOCK (set by Firecracker),
		// so EAGAIN is expected — just go back to poll.
		buf := make([]byte, unsafe.Sizeof(uffdMsg{}))
		n, err := readUffdMsg(uffdFd, buf)
		if err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return fmt.Errorf("read uffd msg: %w", err)
		}
		if n == 0 {
			continue
		}

		msg := *(*uffdMsg)(unsafe.Pointer(&buf[0]))
		if getMsgEvent(&msg) != UFFD_EVENT_PAGEFAULT {
			return fmt.Errorf("unexpected uffd event type: %d", getMsgEvent(&msg))
		}

		arg := getMsgArg(&msg)
		pf := *(*uffdPagefault)(unsafe.Pointer(&arg[0]))
		addr := getPagefaultAddress(&pf)

		offset, pagesize, err := mapping.GetOffset(addr)
		if err != nil {
			return fmt.Errorf("resolve address %#x: %w", addr, err)
		}

		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if err := s.faultPage(ctx, uffdFd, addr, offset, pagesize); err != nil {
				slog.Error("uffd fault page error",
					"addr", fmt.Sprintf("%#x", addr),
					"offset", offset,
					"error", err,
				)
			}
		}()
	}
}

// readUffdMsg reads a single uffd_msg, retrying on EINTR.
// Returns (n, EAGAIN) if the non-blocking read has nothing available.
func readUffdMsg(uffdFd fd, buf []byte) (int, error) {
	for {
		n, err := syscall.Read(int(uffdFd), buf)
		if err == syscall.EINTR {
			continue
		}
		return n, err
	}
}

// faultPage fetches a page from the memory source and copies it into
// guest memory via UFFDIO_COPY.
func (s *Server) faultPage(ctx context.Context, uffdFd fd, addr uintptr, offset int64, pagesize uintptr) error {
	data, err := s.source.ReadPage(ctx, offset, int64(pagesize))
	if err != nil {
		return fmt.Errorf("read page at offset %d: %w", offset, err)
	}

	// Mode 0: no write-protect. Standard Firecracker does not register
	// UFFD ranges with WP support, so UFFDIO_COPY_MODE_WP would fail.
	if err := uffdFd.copy(addr, pagesize, data, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			// Page already mapped (race with prefetch or concurrent fault).
			return nil
		}
		return fmt.Errorf("uffdio_copy: %w", err)
	}

	return nil
}

// DiffFileSource serves pages from a snapshot's compact diff file using
// the header's block mapping to resolve offsets.
type DiffFileSource struct {
	header *snapshot.Header
	// diffs maps build ID → open file handle for each generation's diff file.
	diffs map[string]*os.File
}

// NewDiffFileSource creates a memory source backed by snapshot diff files.
// diffs maps build ID string to the file path of each generation's diff file.
func NewDiffFileSource(header *snapshot.Header, diffPaths map[string]string) (*DiffFileSource, error) {
	diffs := make(map[string]*os.File, len(diffPaths))
	for id, path := range diffPaths {
		f, err := os.Open(path)
		if err != nil {
			// Close already opened files.
			for _, opened := range diffs {
				opened.Close()
			}
			return nil, fmt.Errorf("open diff file %s: %w", path, err)
		}
		diffs[id] = f
	}
	return &DiffFileSource{header: header, diffs: diffs}, nil
}

// ReadPage resolves a memory offset through the header mapping and reads
// the corresponding page from the correct generation's diff file.
func (s *DiffFileSource) ReadPage(ctx context.Context, offset int64, size int64) ([]byte, error) {
	mappedOffset, _, buildID, err := s.header.GetShiftedMapping(ctx, offset)
	if err != nil {
		return nil, fmt.Errorf("resolve offset %d: %w", offset, err)
	}

	// uuid.Nil means zero-fill (empty page).
	var nilUUID [16]byte
	if *buildID == nilUUID {
		return make([]byte, size), nil
	}

	f, ok := s.diffs[buildID.String()]
	if !ok {
		return nil, fmt.Errorf("no diff file for build %s", buildID)
	}

	buf := make([]byte, size)
	n, err := f.ReadAt(buf, mappedOffset)
	if err != nil && int64(n) < size {
		return nil, fmt.Errorf("read diff at offset %d: %w", mappedOffset, err)
	}

	return buf, nil
}

// Close closes all open diff file handles.
func (s *DiffFileSource) Close() error {
	var errs []error
	for _, f := range s.diffs {
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
