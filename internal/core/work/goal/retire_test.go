// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"fmt"
	"testing"
)

// Active and settled actions cannot retire. Their reconciliation cost must
// not multiply the size of a parallel graph by the number of worker actions.
func TestRetirementSkipsUnchangedParallelGraphs(t *testing.T) {
	for _, action := range []Action{
		{State: "claimed"},
		{State: "running"},
		{State: "unknown"},
		{State: "awaiting_authorization"},
		{State: "needs_person"},
		{State: "finished"},
		{State: "finished", Superseded: true, RetirementReconciled: true},
	} {
		t.Run(fmt.Sprintf("%s/reconciled=%t", action.State, action.RetirementReconciled), func(t *testing.T) {
			measure := func(steps int) float64 {
				t.Helper()
				v := &View{records: map[string]Record{}, changes: map[string]Record{}}
				g := Goal{ID: "goal", RecordID: "definition", RecordRevision: "revision", Revision: 1}
				for i := range steps {
					g.Tasks = append(g.Tasks, Task{ID: fmt.Sprintf("step-%d", i), Stage: TaskBuilding, Session: fmt.Sprintf("worker-%d", i)})
				}
				if err := put(v, "goal", g.ID, g); err != nil {
					t.Fatal(err)
				}
				for i := range 82 {
					a := action
					a.ID, a.GoalID, a.TaskID = fmt.Sprintf("worker-%d", i), g.ID, "step-0"
					if err := put(v, "action", a.ID, a); err != nil {
						t.Fatal(err)
					}
				}
				v.changes = map[string]Record{}
				e := &Engine{}
				e.definitions.Store(definitionKey{g.RecordID, g.RecordRevision}, savedDefinition{revision: g.RecordRevision})
				allocs := testing.AllocsPerRun(3, func() {
					if err := e.reconcileRetired(v); err != nil {
						t.Fatal(err)
					}
				})
				if len(v.changes) != 0 {
					t.Fatal("unchanged actions modified the ledger")
				}
				return allocs
			}
			small, fanout := measure(1), measure(82)
			t.Logf("82 unchanged actions: one-step graph %.0f allocations, 82-step graph %.0f", small, fanout)
			// Leave room for runtime bookkeeping without allowing every action
			// to decode the whole goal again. This is independent of CPU speed.
			if fanout > 2*small {
				t.Fatalf("retirement cost grows with an unchanged graph: %.0f -> %.0f allocations", small, fanout)
			}
		})
	}
}

func TestRetirementKeepsAssessmentRulesAtEachSite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		revision       bool
		reconciled     bool
		repairedBy     string
		wantAsked      bool
		wantReconciled bool
	}{
		{name: "report clears pending assessment"},
		{name: "late report keeps replacement assessment", reconciled: true, wantAsked: true, wantReconciled: true},
		{name: "report keeps existing repair", repairedBy: "repair", wantAsked: true},
		{name: "revision completes assessment retirement", revision: true, wantReconciled: true},
		{name: "revision keeps existing repair", revision: true, repairedBy: "repair", wantAsked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &View{records: map[string]Record{}, changes: map[string]Record{}}
			g := Goal{ID: "goal", Revision: 2, Tasks: []Task{{ID: "task", RepairAsked: true, RepairedBy: tc.repairedBy}}}
			o := Observation{Status: OutcomeSucceeded, Tasks: []Task{{ID: "obsolete-repair"}}}
			a := Action{ID: "assessment", GoalID: g.ID, TaskID: "task", Kind: PlanGoal, State: "claimed", GoalRevision: 1, RetirementReconciled: tc.reconciled, Receipt: encode(o)}
			if err := put(v, "goal", g.ID, g); err != nil {
				t.Fatal(err)
			}
			if err := put(v, "action", a.ID, a); err != nil {
				t.Fatal(err)
			}
			if tc.revision {
				if err := (&Engine{}).retireRevisedActions(v, &g); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := retireReportedAction(v, g, a, o); err != nil {
					t.Fatal(err)
				}
				var err error
				g, err = loadGoal(v, g.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			retired, err := loadAction(v, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if retired.State != "finished" || !retired.Superseded || retired.RetirementReconciled != tc.wantReconciled || !bytes.Equal(retired.Receipt, a.Receipt) {
				t.Fatalf("assessment retirement lost its receipt or settlement: %+v", retired)
			}
			if len(g.Tasks) != 1 || g.Tasks[0].RepairAsked != tc.wantAsked || g.Tasks[0].RepairedBy != tc.repairedBy {
				t.Fatalf("retirement changed the current repair assessment: %+v", g.Tasks)
			}
		})
	}
}

func TestRetireSignaledMergeKeepsDispatchedEffects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		action Action
		retire bool
	}{
		{"queued merge", Action{Kind: Merge, State: "queued"}, true},
		{"claimed merge", Action{Kind: Merge, State: "claimed"}, false},
		{"running merge", Action{Kind: Merge, State: "running"}, false},
		{"merge observation", Action{Kind: Merge, State: "queued", ObserveActionID: "original"}, false},
		{"queued inspection", Action{Kind: InspectPR, State: "queued"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.action
			if retired := retireSignaledMerge(&a); retired != tc.retire {
				t.Fatalf("retired = %t, want %t", retired, tc.retire)
			}
			if tc.retire {
				if a.State != "finished" || a.Superseded || a.RetirementReconciled {
					t.Fatalf("signal changed retirement semantics: %+v", a)
				}
			} else if !bytes.Equal(encode(a), encode(tc.action)) {
				t.Fatalf("signal changed an action still needing observation: %+v", a)
			}
		})
	}
}
