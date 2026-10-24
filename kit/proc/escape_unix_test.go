// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package proc_test

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

var escapeFinishes = []string{"close", "cancel", "exit"}

// "cleared" leaves the new session and adds a fully cleared environment, so the
// descendant carries neither the owner token nor any inherited entry.
func configureEscape(cmd *exec.Cmd, escape string) {
	if escape == "setpgid" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

func killEscaped(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

func escapedGone(pid int) error {
	// On Darwin the kernel's reaper, not this process, collects an orphan.
	// Poll the actual PID state with a deadline rather than assuming a sleep
	// is enough; Linux's supervisor already joined it before Run returned.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("process still exists: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}
