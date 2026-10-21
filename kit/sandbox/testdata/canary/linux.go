// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

func platformCanary() error {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for _, line := range []string{"NoNewPrivs:\t1\n", "Seccomp:\t2\n"} {
		if !strings.Contains(string(status), line) {
			return fmt.Errorf("confinement not active")
		}
	}
	// unshare(0) normally succeeds and io_uring_setup with a null argument
	// normally fails EFAULT/EINVAL. EPERM proves the installed seccomp policy.
	for _, nr := range []uintptr{syscall.SYS_UNSHARE, 425} {
		_, _, errno := syscall.RawSyscall(nr, 0, 0, 0)
		if errno != syscall.EPERM {
			return fmt.Errorf("seccomp syscall %d: %v", nr, errno)
		}
	}
	return nil
}
