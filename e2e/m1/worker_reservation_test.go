// SPDX-License-Identifier: Apache-2.0
package e2e_test

import "testing"

func TestM3WorkerReservationsThroughInstalledCore(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m3/worker-reservations.json", false)
}
