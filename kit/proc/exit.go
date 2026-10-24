// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"fmt"
	"syscall"
)

// ExitError reports how a command ended when no Go child process owns it.
// macOS commands run as launchd jobs, so their status arrives over the job
// connection; Linux and Windows report *os/exec.ExitError instead.
type ExitError struct {
	Code   int
	Signal syscall.Signal
}

func (e *ExitError) Error() string {
	if e.Signal != 0 {
		return fmt.Sprintf("signal: %v", e.Signal)
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// ExitCode follows os/exec: -1 when the command died from a signal.
func (e *ExitError) ExitCode() int {
	if e.Signal != 0 {
		return -1
	}
	return e.Code
}
