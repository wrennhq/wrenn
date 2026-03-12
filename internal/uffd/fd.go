// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

// Package uffd implements a userfaultfd-based memory server for Firecracker
// snapshot restore. When a VM is restored from a snapshot, instead of loading
// the entire memory file upfront, the UFFD handler intercepts page faults
// and serves memory pages on demand from the snapshot's compact diff file.
package uffd

/*
#include <sys/syscall.h>
#include <fcntl.h>
#include <linux/userfaultfd.h>
#include <sys/ioctl.h>

struct uffd_pagefault {
	__u64 flags;
	__u64 address;
	__u32 ptid;
};
*/
import "C"

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	UFFD_EVENT_PAGEFAULT      = C.UFFD_EVENT_PAGEFAULT
	UFFD_PAGEFAULT_FLAG_WRITE = C.UFFD_PAGEFAULT_FLAG_WRITE
	UFFDIO_COPY               = C.UFFDIO_COPY
	UFFDIO_COPY_MODE_WP       = C.UFFDIO_COPY_MODE_WP
)

type (
	uffdMsg       = C.struct_uffd_msg
	uffdPagefault = C.struct_uffd_pagefault
	uffdioCopy    = C.struct_uffdio_copy
)

// fd wraps a userfaultfd file descriptor received from Firecracker.
type fd uintptr

// copy installs a page into guest memory at the given address using UFFDIO_COPY.
// mode controls write-protection: use UFFDIO_COPY_MODE_WP to preserve WP bit.
func (f fd) copy(addr, pagesize uintptr, data []byte, mode C.ulonglong) error {
	alignedAddr := addr &^ (pagesize - 1)
	cpy := uffdioCopy{
		src:  C.ulonglong(uintptr(unsafe.Pointer(&data[0]))),
		dst:  C.ulonglong(alignedAddr),
		len:  C.ulonglong(pagesize),
		mode: mode,
		copy: 0,
	}

	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(f), UFFDIO_COPY, uintptr(unsafe.Pointer(&cpy)))
	if errno != 0 {
		return errno
	}

	if cpy.copy != C.longlong(pagesize) {
		return fmt.Errorf("UFFDIO_COPY copied %d bytes, expected %d", cpy.copy, pagesize)
	}

	return nil
}

// close closes the userfaultfd file descriptor.
func (f fd) close() error {
	return syscall.Close(int(f))
}

// getMsgEvent extracts the event type from a uffd_msg.
func getMsgEvent(msg *uffdMsg) C.uchar {
	return msg.event
}

// getMsgArg extracts the arg union from a uffd_msg.
func getMsgArg(msg *uffdMsg) [24]byte {
	return msg.arg
}

// getPagefaultAddress extracts the faulting address from a uffd_pagefault.
func getPagefaultAddress(pf *uffdPagefault) uintptr {
	return uintptr(pf.address)
}
