// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func peerCredentials(conn *net.UnixConn) (uid, pid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var socketErr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			socketErr = e
			return
		}
		uid = int(cred.Uid)
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err != nil {
		return 0, 0, err
	}
	return uid, pid, socketErr
}

func inspectProcess(pid int) (processIdentity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return processIdentity{}, err
	}
	if int(info.Proc.P_pid) != pid {
		return processIdentity{}, fmt.Errorf("process identity unavailable")
	}
	return processIdentity{parent: int(info.Eproc.Ppid), start: fmt.Sprint(info.Proc.P_starttime)}, nil
}
