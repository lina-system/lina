// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configure(c *exec.Cmd) error { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}; return nil }
func kill(c *exec.Cmd) error {
	err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
