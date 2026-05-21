// Package sandbox: post-snapshot hole punching for memory-ranges files.
//
// CH v52's SEEK_DATA/SEEK_HOLE snapshot writer only skips ranges already
// hole in the source memfd. Pages the guest never reported as free are
// written verbatim — including pages whose contents happen to be all zero
// (fresh allocations the guest scribbled then released without telling the
// balloon driver). Walking the resulting file and punching any 4 KiB block
// of zeros recovers that space without any guest cooperation.
package sandbox

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// punchBlockSize is the granularity at which we test for zero runs and
	// issue FALLOC_FL_PUNCH_HOLE. Matches the kernel page size and the
	// minimum hole size on ext4.
	punchBlockSize = 4096

	// punchReadSize is the IO chunk size used by the scan loop. We read
	// many blocks per syscall and split them in-memory so a 20 GiB
	// memory-ranges file costs ~20K read(2) syscalls instead of ~5M.
	// Crucial under single-disk hosts where each syscall otherwise
	// contends with sshd / journal IO.
	punchReadSize = 1 << 20 // 1 MiB = 256 blocks
)

// punchZeroPagesInDir runs punchZeroPages on every memory* file in dir.
// CH writes its memory dump as one or more files prefixed "memory" inside
// the snapshot directory; everything else (config.json, state.json) is
// metadata and untouched.
func punchZeroPagesInDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Warn("punch: read snapshot dir", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "memory") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		before, after, err := punchZeroPages(path)
		if err != nil {
			slog.Warn("punch: zero-page scan failed", "path", path, "error", err)
			continue
		}
		slog.Info("punch: zero-page scan done",
			"path", path,
			"alloc_before", before,
			"alloc_after", after,
			"reclaimed", before-after)
	}
}

// punchZeroPages scans path block-by-block, batching runs of all-zero 4 KiB
// blocks and punching them out via FALLOC_FL_PUNCH_HOLE. Existing holes are
// skipped via SEEK_DATA so a partially-sparse input stays cheap to scan.
//
// Returns the file's disk allocation (st_blocks * 512) before and after.
func punchZeroPages(path string) (int64, int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	stBefore, err := statBlocks(f)
	if err != nil {
		return 0, 0, fmt.Errorf("stat before: %w", err)
	}

	fi, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat: %w", err)
	}
	size := fi.Size()

	buf := make([]byte, punchReadSize)
	off := int64(0)

	for off < size {
		// Skip ahead to next data region; nothing to do in holes.
		next, err := f.Seek(off, 3) // SEEK_DATA = 3
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, unix.ENXIO) {
				break
			}
			return 0, 0, fmt.Errorf("seek_data @ %d: %w", off, err)
		}
		off = next &^ (punchBlockSize - 1) // align down to block

		// Find end of this data extent.
		endData, err := f.Seek(off, 4) // SEEK_HOLE = 4
		if err != nil {
			return 0, 0, fmt.Errorf("seek_hole @ %d: %w", off, err)
		}

		// Scan [off, endData) chunk by chunk; batch zero runs across both
		// intra-chunk and inter-chunk boundaries so a contiguous zero
		// region is punched in a single fallocate.
		zeroStart := int64(-1)
		cur := off
		for cur < endData {
			toRead := min(int64(len(buf)), endData-cur)
			n, err := readAt(f, buf[:toRead], cur)
			if err != nil {
				return 0, 0, fmt.Errorf("read @ %d: %w", cur, err)
			}
			if n == 0 {
				break
			}
			// Walk the chunk one block at a time, tracking zero runs.
			for blkOff := 0; blkOff < n; blkOff += punchBlockSize {
				blkEnd := min(blkOff+punchBlockSize, n)
				blk := buf[blkOff:blkEnd]
				blkAbs := cur + int64(blkOff)
				if isZero(blk) && len(blk) == punchBlockSize {
					if zeroStart < 0 {
						zeroStart = blkAbs
					}
				} else if zeroStart >= 0 {
					if err := punch(f, zeroStart, blkAbs-zeroStart); err != nil {
						return 0, 0, err
					}
					zeroStart = -1
				}
			}
			cur += int64(n)
		}
		if zeroStart >= 0 {
			if err := punch(f, zeroStart, cur-zeroStart); err != nil {
				return 0, 0, err
			}
		}
		off = endData
	}

	stAfter, err := statBlocks(f)
	if err != nil {
		return 0, 0, fmt.Errorf("stat after: %w", err)
	}
	return stBefore, stAfter, nil
}

func punch(f *os.File, off, length int64) error {
	mode := uint32(unix.FALLOC_FL_PUNCH_HOLE | unix.FALLOC_FL_KEEP_SIZE)
	if err := unix.Fallocate(int(f.Fd()), mode, off, length); err != nil {
		return fmt.Errorf("fallocate punch @ %d len %d: %w", off, length, err)
	}
	return nil
}

func readAt(f *os.File, buf []byte, off int64) (int, error) {
	n, err := f.ReadAt(buf, off)
	if err == io.EOF {
		return n, nil
	}
	return n, err
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func statBlocks(f *os.File) (int64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	return int64(st.Blocks) * 512, nil
}
