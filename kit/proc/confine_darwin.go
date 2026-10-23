// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// sandboxExec applies a Seatbelt policy to a process and then becomes the
// command with the same pid and process group.
const sandboxExec = "/usr/bin/sandbox-exec"

// EscapeDenial is the Seatbelt rule that refuses the calls with which a child
// leaves its process group. The policy is inherited across fork and exec and no
// process can remove it, so teardown can always reach the whole group. Linux
// gets the same guarantee from its seccomp policy.
const EscapeDenial = "(deny syscall-unix (syscall-number SYS_setsid SYS_setpgid))"

// escapePolicy allows everything a command needs and denies only the escape
// calls. The last matching rule wins, so the denial follows the default allow.
const escapePolicy = "(version 1)\n(allow default)\n" + EscapeDenial + "\n"

// confine wraps a command in the escape-denial policy unless the caller already
// runs it under a launcher that applies the same denial.
func confine(path string, args []string, confined bool) (string, []string, bool) {
	if confined {
		return path, args, false
	}
	wrapped := make([]string, 0, len(args)+3)
	wrapped = append(wrapped, "-p", escapePolicy, path)
	return sandboxExec, append(wrapped, args...), true
}

// checkEscapeDenial runs before the launcher starts. A host without the
// launcher fails closed instead of starting a command it cannot own, and a
// missing or non-executable command gets the kernel's own error instead of the
// launcher's exit status.
func checkEscapeDenial(target string) error {
	if info, err := os.Stat(sandboxExec); err != nil || info.IsDir() {
		return fmt.Errorf("process escape denial needs %s", sandboxExec)
	}
	info, err := os.Stat(target)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return &os.PathError{Op: "fork/exec", Path: target, Err: err}
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return &os.PathError{Op: "fork/exec", Path: target, Err: syscall.EACCES}
	}
	return nil
}
