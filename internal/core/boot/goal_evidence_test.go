// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestCodeGoalExternalReadbackRequiresMatchingRecord(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "external.json")
	command := codeCommand{Path: "/bin/sh", Args: []string{"-c", `cat "$1"`, "read", path}}
	d := &codeGoalDriver{runtime: &conversationRuntime{root: root, activities: lifecycle.NewRegistry(nil)}, connections: codeConnections{EvidenceChecks: map[string]codeEvidenceConnection{"calendar.read": {Repository: "repo", Target: "calendar:event", Kind: goal.ReadEvidence, Command: command}}}}
	g := goal.Goal{Scope: goal.GoalScope{Repositories: []string{"repo"}}}
	a := goal.Action{ID: "read", Revision: "merged", CriterionID: "event", GoalRevision: 1, CheckRound: 1, Check: &goal.CriterionCheck{Kind: goal.ReadEvidence, Method: "calendar.read", Target: "calendar:event", Expected: []byte(`{"time":"09:00","place":"A"}`)}}
	for _, tc := range []struct {
		name, body    string
		valid, passed bool
	}{
		{"matched", `{"time":"09:00","place":"A","id":"event"}`, true, true},
		{"moved", `{"time":"10:00","place":"A","id":"event"}`, true, false},
		{"receipt", `{"accepted":true}`, true, false},
		{"model-sentence", `"event created"`, false, false},
		{"bare-success", ``, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed := true
			output := codeCheckOutput{Revision: a.Revision, Target: a.Check.Target, Passed: &passed, Evidence: "Read calendar record"}
			if tc.body != "" {
				output.Collected = &goal.CollectedEvidence{Record: []byte(tc.body)}
			}
			raw, _ := json.Marshal(output)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			o, err := d.checkCriterion(t.Context(), goal.State{}, g, goal.Task{}, a, codeRepository{Clone: root})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid && (o.Criterion == nil || o.Criterion.Passed != tc.passed || string(o.Criterion.Collected.Record) != tc.body) {
				t.Fatalf("unverified plugin result: %+v", o.Criterion)
			}
		})
	}
}
