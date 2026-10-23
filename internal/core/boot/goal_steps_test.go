// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestGoalStepWaitReadsOwnedRecordAndExpiry(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	data := []byte(fmt.Sprintf("---\nid: \"target\"\nkind: memo\nstate: active\nperson_id: %q\nagent_id: %q\n---\n\n# Record\n", r.person, r.agent))
	revision, err := repo.Commit(t.Context(), records.Change{ID: "target", Path: "inbox/target.md", Data: data}, records.Actor{Person: r.person, Agent: r.agent})
	if err != nil {
		t.Fatal(err)
	}
	d := &codeGoalDriver{runtime: r}
	g := goal.Goal{Person: r.person}
	for _, tc := range []struct {
		name   string
		person string
		wait   goal.StepWait
		status goal.Outcome
	}{
		{"date", r.person, goal.StepWait{At: 1}, goal.OutcomeSucceeded},
		{"changed", r.person, goal.StepWait{RecordID: "target", Revision: "previous", Deadline: time.Now().Add(time.Hour).Unix()}, goal.OutcomeSucceeded},
		{"expired", r.person, goal.StepWait{RecordID: "target", Revision: revision, Deadline: 1}, goal.OutcomeNeedsPerson},
		{"foreign", "other", goal.StepWait{RecordID: "target", Revision: "previous", Deadline: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g.Person = tc.person
			o, err := d.waitStep(t.Context(), g, goal.Task{Wait: &tc.wait}, goal.Action{ID: "wait"})
			if tc.status == "" {
				if err == nil {
					t.Fatal("foreign record observed")
				}
				return
			}
			if err != nil || o.Status != tc.status || !o.EffectsSettled {
				t.Fatalf("observation=%+v error=%v", o, err)
			}
			if tc.status == goal.OutcomeSucceeded && (!o.Proof.Passed || o.Proof.Ref == "" || o.Proof.Revision == "") {
				t.Fatal("unproven close")
			}
		})
	}
}
