// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"errors"
	"fmt"
	"os"
	"sync"
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

var checkSandboxExec sync.Once

// confine wraps a command in the escape-denial policy unless the caller already
// runs it under a launcher that applies the same denial. A host without the
// launcher fails closed instead of starting a command it cannot own.
func confine(path string, args []string, confined bool) (string, []string, error) {
	if confined {
		return path, args, nil
	}
	var missing error
	checkSandboxExec.Do(func() {
		if info, err := os.Stat(sandboxExec); err != nil || info.IsDir() {
			missing = fmt.Errorf("process escape denial needs %s", sandboxExec)
		}
	})
	if missing != nil {
		return "", nil, missing
	}
	// The launcher would report its own failure instead of the command's, so
	// answer a missing or non-executable command here as the kernel would.
	info, err := os.Stat(path)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return "", nil, &os.PathError{Op: "fork/exec", Path: path, Err: err}
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", nil, &os.PathError{Op: "fork/exec", Path: path, Err: syscall.EACCES}
	}
	wrapped := make([]string, 0, len(args)+3)
	wrapped = append(wrapped, "-p", escapePolicy, path)
	return sandboxExec, append(wrapped, args...), nil
}
