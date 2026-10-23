// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"testing"
)

// TestM2InstalledGoalWorkerRecovery runs m2/goal-worker-recovery.json through
// installed Core. A settled failure recovers once without a conversation
// dispatch receipt. The worker commits a real result; startup reconciliation
// delivers it through synthetic GitHub and service wires. A second failure
// becomes a decision and restart cannot launch a third attempt.
func TestM2InstalledGoalWorkerRecovery(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/goal-worker-recovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Modes []string `json:"modes"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, mode := range scenario.Modes {
		t.Run(mode, func(t *testing.T) { t.Parallel(); installedCodeGoal(t, mode) })
	}
}
