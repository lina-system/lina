// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// libproc is reached through dynamic imports so the kit stays cgo-free and a
// static binary keeps working. The trampolines are six words wide because
// proc_pidinfo takes six arguments.
func libc_proc_listallpids_trampoline()
func libc_proc_pidinfo_trampoline()

//go:cgo_import_dynamic libc_proc_listallpids proc_listallpids "/usr/lib/libproc.dylib"
//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libproc.dylib"

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5 uintptr) (r1, r2 uintptr, err syscall.Errno)

func libprocListAllPidsPC() uintptr
func libprocPidinfoPC() uintptr

// procPIDCoalitionInfo is PROC_PIDCOALITIONINFO: the resource coalition of a
// process and the three coalition kinds that follow it.
const procPIDCoalitionInfo = 20

// coalitionIDs returns the coalitions a process belongs to. The first entry is
// the resource coalition that launchd assigns to a job and that every
// descendant inherits.
func coalitionIDs(pid int) ([5]uint64, error) {
	var ids [5]uint64
	r0, _, _ := syscall_syscall6(libprocPidinfoPC(), uintptr(pid), procPIDCoalitionInfo, 0, uintptr(unsafe.Pointer(&ids[0])), uintptr(unsafe.Sizeof(ids)))
	n := int(r0)
	if n < len(ids)*8 {
		return ids, fmt.Errorf("read coalition of process %d: %d of %d bytes", pid, n, len(ids)*8)
	}
	return ids, nil
}

// allPIDs lists every process the kernel reports, regardless of owner.
func allPIDs() ([]int, error) {
	size := 4096
	for {
		buffer := make([]int32, size)
		r0, _, _ := syscall_syscall(libprocListAllPidsPC(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))*4, 0)
		n := int(r0)
		if n < 0 {
			return nil, fmt.Errorf("list processes: %d bytes", n)
		}
		if n < len(buffer) {
			pids := make([]int, n)
			for i := range pids {
				pids[i] = int(buffer[i])
			}
			return pids, nil
		}
		if size > 1<<20 {
			return nil, errors.New("process table does not fit the scan buffer")
		}
		size *= 2
	}
}

func processInfo(pid int) (*unix.KinfoProc, error) {
	return unix.SysctlKinfoProc("kern.proc.pid", pid)
}
