// SPDX-License-Identifier: Apache-2.0
package e2e_test

import "testing"

func TestM4BridgedWorkerThroughInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m4/bridged-worker.json", false)
}
