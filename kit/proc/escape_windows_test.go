// SPDX-License-Identifier: Apache-2.0

package proc_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

var escapeModes = []string{"group", "detached"}

func configureEscape(cmd *exec.Cmd, escape string) {
	flags := uint32(syscall.CREATE_NEW_PROCESS_GROUP)
	if escape == "detached" {
		flags |= 0x8
	} // DETACHED_PROCESS
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

func killEscaped(pid int) {
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err == nil {
		defer syscall.CloseHandle(handle)
		_ = syscall.TerminateProcess(handle, 1)
	}
}

func escapedGone(pid int) error {
	handle, err := syscall.OpenProcess(0x100000, false, uint32(pid)) // SYNCHRONIZE
	if err == syscall.Errno(87) {                                    // ERROR_INVALID_PARAMETER: PID no longer exists
		return nil
	}
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(handle)
	// TerminateJobObject and an empty job establish that execution has ended,
	// but Windows can finish process-object teardown asynchronously. Wait on
	// that actual completion event; a zero-time probe can see WAIT_TIMEOUT.
	state, err := syscall.WaitForSingleObject(handle, 2000)
	if err != nil {
		return err
	}
	if state != syscall.WAIT_OBJECT_0 {
		return fmt.Errorf("process still running: wait state %d", state)
	}
	return nil
}

func TestJobRejectsBreakaway(t *testing.T) {
	owner := proc.NewOwner()
	defer owner.Close(t.Context())
	l := lease.New(func() {})
	defer l.Release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Run(t.Context(), l, proc.Command{Path: exe, Args: []string{"-test.run=^TestJobBreakawayProcess$"}, Env: []string{"LINA_PROC_BREAKAWAY_TEST=1"}, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
}

func TestJobBreakawayProcess(t *testing.T) {
	if os.Getenv("LINA_PROC_BREAKAWAY_TEST") != "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEscapingProcess$", "--", "leaf", "group")
	cmd.Env = []string{"LINA_PROC_ESCAPE_TEST=1"}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x1000000} // CREATE_BREAKAWAY_FROM_JOB
	err := cmd.Start()
	if err == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("child escaped the job")
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatal(err)
	}
}
