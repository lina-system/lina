// SPDX-License-Identifier: Apache-2.0

package e2e_test

import "syscall"

func setHarnessParentDeathSignal(attr *syscall.SysProcAttr) {
	// Cleanup cannot run after os.Exit, a testing timeout, or SIGKILL.
	// start pins the spawning thread until Wait, so ordinary goroutine or
	// runtime-thread retirement cannot kill a still-owned Core prematurely.
	attr.Pdeathsig = syscall.SIGKILL
}
