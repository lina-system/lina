// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"testing"
)

func TestInvalidDefinitionIsolatedAcrossRestart(t *testing.T) {
	for _, missing := range []string{"done", "criteria"} {
		t.Run(missing, func(t *testing.T) {
			e := revisionEngine(t, readyTask("queued"), readyTask("live"))
			live, err := e.Claim(t.Context(), revisionAction(t, e, "live", Assign).ID)
			if err != nil {
				t.Fatal(err)
			}
			queued := revisionAction(t, e, "queued", Assign)
			valid := revisionSnapshot(t, e).Goals[0]
			other := oneGoal(readyTask("other"))
			other.ID = "unrelated"
			if err := e.Admit(t.Context(), other); err != nil {
				t.Fatal(err)
			}
			r, base, err := e.cfg.Records.Read(t.Context(), valid.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			invalid := valid
			if missing == "done" {
				invalid.Done = ""
			} else {
				invalid.Criteria = nil
			}
			rev, err := e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: base, Path: r.Path, Data: definitionBytes(invalid)}, valid.Person, valid.Agent)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatalf("invalid %s correction stopped unrelated derivation: %v", missing, err)
			}
			assertBlocked := func() {
				t.Helper()
				s := revisionSnapshot(t, e)
				g := s.Goals[0]
				if g.ID != valid.ID || g.State != GoalBlocked || !g.DefinitionPending || g.RecordRevision != rev || g.AttentionID == "" {
					t.Fatalf("invalid definition was not isolated: %+v", g)
				}
				if task := g.Tasks[0]; task.Stage != TaskReady || task.Attempt != 0 || task.Recoveries != 0 {
					t.Fatalf("invalid correction spent undispatched reservation: %+v", task)
				}
				for _, a := range s.Actions {
					if a.GoalID == g.ID && a.State == "queued" && a.ObserveActionID == "" {
						t.Fatalf("invalid goal dispatched fresh work: %+v", a)
					}
				}
			}
			assertBlocked()
			if _, err := e.Claim(t.Context(), queued.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("obsolete assignment remains claimable: %v", err)
			}
			cfg := e.cfg
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatalf("invalid definition stopped startup: %v", err)
			}
			assertBlocked()
			// Both another goal's worker and the quarantined goal's outstanding
			// effect can report. The latter is read again under the current fence.
			revisionReport(t, e, "other", Assign, Observation{Status: OutcomeSucceeded, Head: "other-head", EffectsSettled: true})
			receipt := Observation{Status: OutcomeSucceeded, Head: "live-head", EffectsSettled: true}
			if err := e.Report(t.Context(), live.ID, cfg.Epoch, receipt); err != nil {
				t.Fatal(err)
			}
			observeRevisedWorker(t, e, live, receipt)
			assertBlocked()
			if _, err := e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(valid)}, valid.Person, valid.Agent); err != nil {
				t.Fatal(err)
			}
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("extra")}})
			replacement := revisionAction(t, e, "queued", Assign)
			if replacement.Attempt != queued.Attempt || replacement.ID == queued.ID {
				t.Fatalf("valid correction did not restore original attempt: %+v", replacement)
			}
			if _, err := e.Claim(t.Context(), replacement.ID); err != nil {
				t.Fatalf("corrected goal cannot resume: %v", err)
			}
		})
	}
}

func TestPreviouslyInstalledInvalidDefinitionQuarantinedOnStartup(t *testing.T) {
	e := revisionEngine(t, readyTask("queued"))
	_ = revisionAction(t, e, "queued", Assign)
	g := revisionSnapshot(t, e).Goals[0]
	r, base, err := e.cfg.Records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	g.Criteria = nil
	rev, err := e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: base, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the prior Core's committed revision event. Replaying that same
	// event cannot install the new quarantine because the journal deduplicates it.
	err = e.journal.Update(t.Context(), Transition{ID: "goal.record:" + g.ID + ":" + rev, Kind: "goal.revised", At: 100}, func(v *View) error {
		g.RecordRevision, g.DefinitionPending = rev, true
		g.Revision++
		if err := retireUndispatchedActions(v, &g); err != nil {
			return err
		}
		return put(v, "goal", g.ID, g)
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Epoch++
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatalf("previously installed invalid revision still blocks startup: %v", err)
	}
	before := revisionSnapshot(t, e)
	if before.Goals[0].State != GoalBlocked || before.Goals[0].AttentionID == "" {
		t.Fatal("previously installed invalid revision was not quarantined")
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := revisionSnapshot(t, e)
	if after.Goals[0].Revision != before.Goals[0].Revision || len(after.Actions) != len(before.Actions) {
		t.Fatal("unchanged invalid definition repeated its revision or work")
	}
}
