// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"testing"
)

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
