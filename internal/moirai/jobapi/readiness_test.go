// SPDX-License-Identifier: Apache-2.0
package jobapi

import "testing"

func TestReadinessRequiresLiveCore(t *testing.T) {
	if err := NewClient(t.TempDir() + "/absent.sock").Ready(t.Context()); err == nil {
		t.Fatal("absent Core passed readiness")
	}
}
