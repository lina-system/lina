// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"fmt"
	"testing"
)

func TestCorrectedPlanAttemptIdentitySurvivesRetirement(t *testing.T) {
	t.Parallel()
	for _, executed := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			t.Run(fmt.Sprintf("executed=%v/removed=%v", executed, remove), func(t *testing.T) {
				e := revisionEngine(t, readyTask("a"))
				old := revisionAction(t, e, "a", Assign)
				if executed {
					old = revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "old", EffectsSettled: true})
				}
				before := revisionSnapshot(t, e)
				revisionEdit(t, e)
				if remove {
					revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded})
					revisionEdit(t, e)
				}
				replacement := readyTask("a")
				replacement.Done = "revised instructions"
				revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
				// Restart before consuming the replacement's award.
				cfg := e.cfg
				cfg.Epoch++
				var err error
				e, err = NewEngine(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Reconcile(t.Context()); err != nil {
					t.Fatal(err)
				}
				next := revisionAction(t, e, "a", Assign)
				want := 1
				if executed {
					want = old.Attempt + 1
				}
				if next.Attempt != want {
					t.Fatalf("replacement attempt=%d want=%d (old=%d)", next.Attempt, want, old.Attempt)
				}
				for _, a := range revisionSnapshot(t, e).Actions {
					if a.ID == old.ID && (!a.Superseded || !a.RetirementReconciled) {
						t.Fatalf("obsolete task receipt still eligible: %+v", a)
					}
				}
				if executed {
					if err = e.Report(t.Context(), old.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Head: "late", EffectsSettled: true}); err != nil {
						t.Fatal(err)
					}
					for _, a := range revisionSnapshot(t, e).Actions {
						if a.ID == old.ID {
							// Locate the original receipt by ID; snapshot order is not causal order.
							for _, prior := range before.Actions {
								if prior.ID == a.ID && !bytes.Equal(a.Receipt, prior.Receipt) {
									t.Fatal("late report replaced settled history")
								}
							}
						}
					}
				}
			})
		}
	}
}
