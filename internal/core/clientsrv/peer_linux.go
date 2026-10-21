// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func peerCredentials(conn *net.UnixConn) (uid, pid int, err error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var cred *unix.Ucred
	var socketErr error
	if err = raw.Control(func(fd uintptr) { cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, 0, err
	}
	if socketErr != nil {
		return 0, 0, socketErr
	}
	return int(cred.Uid), int(cred.Pid), nil
}

func inspectProcess(pid int) (processIdentity, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processIdentity{}, err
	}
	return parseProcessStat(string(data))
}

func parseProcessStat(data string) (processIdentity, error) {
	// comm may itself contain spaces and parentheses. Fields after its last ')'
	// start at field 3 (state); ppid is field 4 and starttime is field 22.
	end := strings.LastIndexByte(data, ')')
	if end < 0 {
		return processIdentity{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(data[end+1:])
	if len(fields) < 20 {
		return processIdentity{}, fmt.Errorf("short process stat")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return processIdentity{}, fmt.Errorf("invalid process parent")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, fmt.Errorf("invalid process start time")
	}
	return processIdentity{parent: parent, start: strconv.FormatUint(start, 10)}, nil
}
