// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestLateReportKeepsSettledAssignmentTerminal(t *testing.T) {
	t.Parallel()
	for _, stage := range []TaskStage{TaskMerged, TaskDone} {
		for _, restart := range []bool{false, true} {
			for _, status := range []Outcome{OutcomeSucceeded, OutcomeUnknown} {
				t.Run(fmt.Sprintf("%s/restart=%t/%s", stage, restart, status), func(t *testing.T) {
					e := revisionEngine(t, readyTask("a"), readyTask("b"))
					accepted := Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true}
					worker := revisionReport(t, e, "a", Assign, accepted)
					revisionReport(t, e, "a", InspectPR, revisionPR("head-a"))
					revisionReport(t, e, "a", Merge, Observation{Status: OutcomeSucceeded, Head: "head-a", Proof: Proof{Revision: "merge-a", Ref: "merge", Passed: true}})
					if stage == TaskDone {
						revisionReport(t, e, "a", RunQA, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merge-a", Ref: "qa", Passed: true}})
					}
					if err := e.Signal(t.Context(), Signal{ID: "late-task-hint", GoalID: "g", TaskID: "a"}); err != nil {
						t.Fatal(err)
					}
					before := revisionSnapshot(t, e)
					var settled Action
					for _, a := range before.Actions {
						if a.ID == worker.ID {
							settled = a
						}
					}
					if settled.State != "finished" || settled.Superseded || !staleInputs(before.Goals[0], settled) {
						t.Fatal("fixture must have a settled action with obsolete inputs")
					}
					late := Observation{Status: status, Head: "head-a", Handle: "late-worker-handle", EffectsSettled: status == OutcomeSucceeded}
					if err := e.Report(t.Context(), worker.ID, e.cfg.Epoch, late); err != nil {
						t.Fatal(err)
					}
					// The append-only event retains the new receipt even though the
					// accepted settlement and task projection must stay unchanged.
					if err := e.cfg.DB.Read(t.Context(), func(tx *canon.Tx) error {
						var raw []byte
						if err := tx.QueryRowContext(t.Context(), "SELECT body FROM work_events WHERE kind='action.observed' ORDER BY seq DESC LIMIT 1").Scan(&raw); err != nil {
							return err
						}
						var saved entry
						if err := json.Unmarshal(raw, &saved); err != nil {
							return err
						}
						var data struct{ Observation Observation }
						if err := json.Unmarshal(saved.Event.Data, &data); err != nil {
							return err
						}
						if !bytes.Equal(encode(data.Observation), encode(late)) {
							t.Error("late report was not retained in history")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if restart {
						if err := e.journal.Rebuild(t.Context()); err != nil {
							t.Fatal(err)
						}
						cfg := e.cfg
						cfg.Epoch++
						var err error
						e, err = NewEngine(cfg)
						if err != nil {
							t.Fatal(err)
						}
						// Core reconciles and loads canonical definitions before
						// publishing the restarted engine's report ingress.
						reviewReconcile(t, e)
					}
					for range 2 {
						if err := e.Report(t.Context(), worker.ID, e.cfg.Epoch, late); err != nil {
							t.Fatal(err)
						}
						reviewReconcile(t, e)
					}
					for _, a := range revisionSnapshot(t, e).Actions {
						if a.ID == worker.ID && !bytes.Equal(encode(a), encode(settled)) {
							t.Errorf("late report changed terminal settlement: state=%s superseded=%t", a.State, a.Superseded)
						}
						if a.ObserveActionID == worker.ID {
							t.Error("settled worker acquired a replacement observation")
							// Show the resulting regression, not only the extra read.
							claimed, err := e.Claim(t.Context(), a.ID)
							if err != nil {
								t.Fatal(err)
							}
							if err = e.Report(t.Context(), claimed.ID, claimed.Epoch, accepted); err != nil {
								t.Fatal(err)
							}
						}
					}
					got := revisionSnapshot(t, e).Goals[0].Tasks[0]
					if !bytes.Equal(encode(got), encode(before.Goals[0].Tasks[0])) {
						t.Fatalf("late report changed task: stage=%s, want %s", got.Stage, stage)
					}
				})
			}
		}
	}
}
