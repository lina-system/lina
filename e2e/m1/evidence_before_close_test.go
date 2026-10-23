// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"testing"
)

// TestM2EvidenceBeforeClose uses the installed-Core harness with stub workers
// and read-only plugin commands. The plugin deliberately claims success for a
// wrong record. A rubric check changes an already-read event behind a barrier;
// final readback must reopen it. A completed goal is also rechecked on restart.
func TestM2EvidenceBeforeClose(t *testing.T) {
	scenario := evidenceScenario(t)
	for _, mode := range scenario.Modes {
		t.Run(mode, func(t *testing.T) { installedCodeGoal(t, mode) })
	}
}

func evidenceScenario(t *testing.T) struct {
	Modes    []string
	Criteria []any
} {
	t.Helper()
	raw, err := os.ReadFile("../m2/evidence-before-close.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Modes    []string
		Criteria []any
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}
