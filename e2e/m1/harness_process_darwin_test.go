// SPDX-License-Identifier: Apache-2.0

package e2e_test

import "syscall"

// Darwin has no Pdeathsig. The shared process-group cancellation and Cleanup
// paths still stop Core and descendants on normal test completion/failure.
func setHarnessParentDeathSignal(_ *syscall.SysProcAttr) {}
