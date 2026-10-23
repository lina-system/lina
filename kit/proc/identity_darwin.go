// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The proc_info syscall, its call numbers and its flavors come from XNU's
// bsd/sys/proc_info.h and bsd/sys/proc_info_private.h. Flavor 17 reports the
// identity XNU records at fork: p_puniqueid names the parent's unique process
// id, is copied from the parent and is never rewritten, so a parent chain
// survives the parent's exit, reparenting to launchd, a new session or process
// group, and a cleared environment.
const (
	procInfoCallListPIDs      = 0x1
	procInfoCallPIDInfo       = 0x2
	procAllPIDs               = 0x1
	procPIDShortBSDInfo       = 13
	procShortBSDInfoSize      = 60 // struct proc_bsdshortinfo
	procPIDUniqIdentifierInfo = 17
	procUniqueIDSize          = 56 // struct proc_uniqidentifierinfo
	procStatusZombie          = 5  // SZOMB
)

// processState is one process as the kernel reports it. Only the recorded
// identities and the zombie flag matter for cleanup.
type processState struct {
	processIdentity
	zombie bool
}

// procInfo calls proc_info(2). The kernel returns -1 with errno on failure, so
// the value decides success and an errno left over from an earlier call is
// ignored.
func procInfo(call, pid, flavor uint32, arg uint64, buf []byte) error {
	var ptr unsafe.Pointer
	if len(buf) > 0 {
		ptr = unsafe.Pointer(&buf[0])
	}
	r1, _, errno := unix.Syscall6(unix.SYS_PROC_INFO, uintptr(call), uintptr(pid), uintptr(flavor), uintptr(arg), uintptr(ptr), uintptr(len(buf)))
	if r1 == ^uintptr(0) {
		return errno
	}
	return nil
}

// processIdentityOf reads one process's recorded identity. A nonzero argument
// also lets the kernel answer for a zombie, so an exited but unreaped leader
// keeps its identity through cleanup.
func processIdentityOf(pid int) (processIdentity, error) {
	buf := make([]byte, procUniqueIDSize)
	if err := procInfo(procInfoCallPIDInfo, uint32(pid), procPIDUniqIdentifierInfo, 1, buf); err != nil {
		return processIdentity{}, err
	}
	return processIdentity{
		pid:            pid,
		uniqueID:       binary.LittleEndian.Uint64(buf[16:24]),
		parentUniqueID: binary.LittleEndian.Uint64(buf[24:32]),
	}, nil
}

// processStates reads the identity and zombie flag of every process. Both
// flavors skip the same-user check, so an unprivileged owner still sees a
// descendant that changed its credentials.
func processStates() ([]processState, error) {
	pids, err := processIDs()
	if err != nil {
		return nil, err
	}
	states := make([]processState, 0, len(pids))
	var buf [procShortBSDInfoSize]byte
	for _, pid := range pids {
		identity, err := processIdentityOf(pid)
		if err != nil {
			continue // exited between the listing and the read
		}
		state := processState{processIdentity: identity}
		if err := procInfo(procInfoCallPIDInfo, uint32(pid), procPIDShortBSDInfo, 1, buf[:]); err == nil {
			state.zombie = binary.LittleEndian.Uint32(buf[12:16]) == procStatusZombie
		}
		states = append(states, state)
	}
	return states, nil
}

// processIDs lists every process on the host. The kernel writes a dense prefix
// of int32 pids and reports nothing about truncation, so a completely filled
// buffer is retried larger instead of silently dropping the tail.
func processIDs() ([]int, error) {
	size := 16 << 10 // four thousand processes
	for range 5 {
		buf := make([]byte, size)
		if err := procInfo(procInfoCallListPIDs, procAllPIDs, 0, 0, buf); err != nil {
			return nil, err
		}
		pids := make([]int, 0, len(buf)/4)
		for off := 0; off+4 <= len(buf); off += 4 {
			if pid := int(int32(binary.LittleEndian.Uint32(buf[off:]))); pid > 0 {
				pids = append(pids, pid)
			}
		}
		if len(pids)*4 < len(buf) {
			return pids, nil
		}
		size *= 2
	}
	return nil, unix.E2BIG
}
