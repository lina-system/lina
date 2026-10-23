// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAdmitRejectsAmbiguousServiceRepository(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	service, docs := readyTask("service"), readyTask("docs")
	docs.Repository = "docs"
	g := oneGoal(service, docs)
	g.Scope.Repositories = []string{"repo", "docs"}
	g.NoServiceReason = ""
	g.ServiceTarget = "private-service"
	if err = e.Admit(t.Context(), g); !errors.Is(err, ErrInvalid) {
		t.Fatalf("admitted service without its repository binding: %v", err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Goals) != 0 {
		t.Fatal("invalid admission persisted a goal")
	}
}

// Decode the additive field as wire data so this regression also runs against
// the previous schema, where it was silently discarded.
func bindServiceRepository(t *testing.T, g *Goal, repo string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"service_repository": repo})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, g); err != nil {
		t.Fatal(err)
	}
}

func deploymentEngine(t *testing.T, g Goal) *Engine {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestServiceDeploymentRevisionFollowsRepository(t *testing.T) {
	for _, order := range [][]string{{"service", "docs", "service-update", "docs-update"}, {"docs", "service", "docs-update", "service-update"}} {
		t.Run(order[0]+"_first", func(t *testing.T) {
			var tasks []Task
			for _, id := range order {
				task := readyTask(id)
				if strings.HasPrefix(id, "docs") {
					task.Repository = "docs"
				}
				tasks = append(tasks, task)
			}
			g := oneGoal(tasks...)
			g.Scope.Repositories = []string{"repo", "docs"}
			g.ServiceTarget, g.NoServiceReason = "private-service", ""
			bindServiceRepository(t, &g, "repo")
			e := deploymentEngine(t, g)
			want := ""
			for _, id := range order {
				revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
				revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
				revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merged-" + id, Ref: "merge:" + id, Passed: true}})
				if strings.HasPrefix(id, "service") {
					want = "merged-" + id
				}
				if got := revisionSnapshot(t, e).Goals[0].TargetRevision; got != want {
					t.Fatalf("after %s merge: service target revision=%q, want %q", id, got, want)
				}
				revisionReport(t, e, id, RunQA, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merged-" + id, Ref: "qa:" + id, Passed: true}})
			}
			// Restart before dispatch to exercise the persisted repository binding.
			cfg := e.cfg
			cfg.Epoch++
			var err error
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			saved := revisionSnapshot(t, e).Goals[0]
			if saved.MergedRevisions["repo"] != "merged-service-update" || saved.MergedRevisions["docs"] != "merged-docs-update" {
				t.Fatalf("restart lost per-repository revisions: %+v", saved.MergedRevisions)
			}
			for _, kind := range []ActionKind{Deploy, Verify} {
				a := claimOnlyAction(t, e, kind)
				if a.Revision != "merged-service-update" {
					t.Fatalf("%s uses foreign revision: %+v", kind, a)
				}
				wrong := Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merged-docs-update", Ref: "wrong-repository", Passed: true}}
				if err = e.Report(t.Context(), a.ID, cfg.Epoch, wrong); !errors.Is(err, ErrInvalid) {
					t.Fatalf("accepted foreign %s evidence: %v", kind, err)
				}
				if err = e.Report(t.Context(), a.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: a.Revision, Ref: string(kind) + ":service", Passed: true}}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNoServiceMultiRepositoryKeepsLastMergeRevision(t *testing.T) {
	service, docs := readyTask("service"), readyTask("docs")
	docs.Repository = "docs"
	g := oneGoal(service, docs)
	g.Scope.Repositories = []string{"repo", "docs"}
	e := deploymentEngine(t, g)
	for _, id := range []string{"service", "docs"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
		revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merged-" + id, Ref: "merge:" + id, Passed: true}})
		revisionReport(t, e, id, RunQA, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merged-" + id, Ref: "qa:" + id, Passed: true}})
	}
	if a := claimOnlyAction(t, e, Verify); a.Revision != "merged-docs" {
		t.Fatalf("changed no-service behaviour: %+v", a)
	}
}

func TestServiceRepositoryAdmissionAndRecord(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repos   []string
		binding string
		valid   bool
	}{
		{"single_default", []string{"repo"}, "", true},
		{"single_explicit", []string{"repo"}, "repo", true},
		{"multiple_explicit", []string{"repo", "docs"}, "repo", true},
		{"outside_scope", []string{"repo", "docs"}, "foreign", false},
		{"single_outside_scope", []string{"repo"}, "foreign", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db := testJournal(t)
			e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
			if err != nil {
				t.Fatal(err)
			}
			g := oneGoal(readyTask("service"))
			g.Scope.Repositories = tc.repos
			g.ServiceTarget, g.NoServiceReason = "private-service", ""
			g.ServiceRepository = tc.binding
			err = e.Admit(t.Context(), g)
			if !tc.valid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid binding accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := revisionSnapshot(t, e).Goals[0]
			if got.ServiceRepository != "repo" {
				t.Fatalf("missing binding: %+v", got)
			}
			r, _, err := db.Definitions.Read(t.Context(), got.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			d, err := parseDefinition(r, got.Person, got.Agent)
			if err != nil || d.ServiceRepository != "repo" {
				t.Fatalf("canonical definition lost binding: %+v / %v", d, err)
			}
		})
	}
}

func TestLegacySingleRepositoryServiceRecordLoadsUnchanged(t *testing.T) {
	_, db := testJournal(t)
	cfg := Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.NoServiceReason, g.ServiceTarget, g.Verification = "", "private-service", Proof{}
	// Both the canonical record and execution journal omit the additive fields.
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	before, rev, err := db.Definitions.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before.Data), "service_repository") {
		t.Fatal("fixture is not a legacy definition")
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "legacy", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := claimOnlyAction(t, e, Deploy)
	got := revisionSnapshot(t, e).Goals[0]
	if a.Revision != "merged" || got.ServiceRepository != "repo" {
		t.Fatalf("legacy service lost target: %+v / %+v", a, got)
	}
	after, currentRev, err := db.Definitions.Read(t.Context(), g.RecordID)
	if err != nil || currentRev != rev || !bytes.Equal(before.Data, after.Data) {
		t.Fatalf("loading rewrote the old record: %v", err)
	}
}

func TestAdmitRejectsSuppliedRepositoryMergeState(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.MergedRevisions = map[string]string{"repo": "unobserved"}
	if err = e.Admit(t.Context(), g); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fabricated merge state admitted: %v", err)
	}
}

func TestServiceRepositoryCorrectionInvalidatesSameCommitProofs(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.ServiceTarget, g.NoServiceReason, g.ServiceRepository = "private-service", "", "repo"
	g.Scope.Repositories = []string{"repo", "docs"}
	g.MergedRevisions = map[string]string{"repo": "merged", "docs": "merged"}
	g.Deployment = Proof{Revision: "merged", Ref: "old-repository-deploy", Passed: true}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	r, rev, err := db.Definitions.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	g.ServiceRepository = "docs"
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := revisionSnapshot(t, e).Goals[0]
	if got.Deployment != (Proof{}) || got.Verification != (Proof{}) {
		t.Fatalf("repository correction reused previous service proofs: %+v / %+v", got.Deployment, got.Verification)
	}
}

func TestLegacyServiceRevisionSurvivesAddedRepository(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.ServiceTarget, g.NoServiceReason, g.Verification = "private-service", "", Proof{}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	r, rev, err := db.Definitions.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	g.Scope.Repositories, g.ServiceRepository = []string{"repo", "docs"}, "repo"
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := revisionSnapshot(t, e).Goals[0]
	if got.TargetRevision != "merged" || got.MergedRevisions["repo"] != "merged" {
		t.Fatalf("scope expansion lost legacy service merge: target=%q map=%v", got.TargetRevision, got.MergedRevisions)
	}
}

func TestLateDeploymentCannotVerifyAnotherRepositoryAtSameCommit(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.ServiceTarget, g.NoServiceReason, g.ServiceRepository, g.Verification = "private-service", "", "repo", Proof{}
	g.Scope.Repositories = []string{"repo", "docs"}
	g.MergedRevisions = map[string]string{"repo": "merged", "docs": "merged"}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	a := claimOnlyAction(t, e, Deploy)
	r, rev, err := db.Definitions.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	g.ServiceRepository = "docs"
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, a.Epoch, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merged", Ref: "old-repository-deploy", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	s := revisionSnapshot(t, e)
	if s.Goals[0].Deployment != (Proof{}) || s.Actions[0].State != "finished" {
		t.Fatalf("late deployment verified the new repository: %+v / %+v", s.Goals[0].Deployment, s.Actions)
	}
	var receipt Observation
	if err := json.Unmarshal(s.Actions[0].Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(s.Goals[0].Evidence) != 0 || receipt.Proof.Ref != "old-repository-deploy" || !receipt.Proof.Passed {
		t.Fatal("obsolete deployment must remain receipt history, not current goal evidence")
	}
}

func TestLegacyServiceAdmissionRecoversPublishedDefinition(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	input := oneGoal(readyTask("service"))
	input.ServiceTarget, input.NoServiceReason = "private-service", ""
	legacy := input
	// A prior process published the canonical record but stopped before the
	// journal admission. Retrying the old input must keep that same record.
	if err = e.prepareDefinition(t.Context(), &legacy); err != nil {
		t.Fatal(err)
	}
	before, rev, err := db.Definitions.Read(t.Context(), legacy.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), input); err != nil {
		t.Fatalf("could not recover legacy admission: %v", err)
	}
	after, currentRev, err := db.Definitions.Read(t.Context(), legacy.RecordID)
	if err != nil || rev != currentRev || !bytes.Equal(before.Data, after.Data) {
		t.Fatalf("admission rewrote legacy definition: %v", err)
	}
}
