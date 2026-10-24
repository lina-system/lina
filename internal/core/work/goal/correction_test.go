// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func TestGoalCorrectionPreservesAndUpdatesServiceRepository(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.Scope.Repositories = []string{"repo", "other"}
	g.NoServiceReason, g.ServiceTarget, g.ServiceRepository = "", "private-service", "repo"
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	before := revisionSnapshot(t, e).Goals[0]
	d := definition(before)
	d.ServiceRepository = "other"
	receipt, err := e.Revise(t.Context(), g.ID, before.RecordRevision, g.Person, g.Agent, d)
	if err != nil {
		t.Fatal(err)
	}
	updated := revisionSnapshot(t, e).Goals[0]
	if updated.ServiceRepository != "other" || updated.RecordRevision != receipt.Revision || !updated.DefinitionPending {
		t.Fatalf("correction lost the canonical service binding: %+v", updated)
	}
	d.ServiceRepository = ""
	d.Outcome = "clarified service goal"
	receipt, err = e.Revise(t.Context(), g.ID, updated.RecordRevision, g.Person, g.Agent, d)
	if err != nil {
		t.Fatal(err)
	}
	if got := revisionSnapshot(t, e).Goals[0]; got.ServiceRepository != "other" {
		t.Fatal("omitting the optional binding changed its repository")
	}
	d.ServiceRepository = "outside"
	if _, err = e.Revise(t.Context(), g.ID, receipt.Revision, g.Person, g.Agent, d); err == nil {
		t.Fatal("correction published an out-of-scope service binding")
	}
}

func TestGoalCorrectionKeepsBindingFromLatestCanonicalRevision(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.Scope.Repositories = []string{"repo", "other"}
	g.NoServiceReason, g.ServiceTarget, g.ServiceRepository = "", "private-service", "repo"
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	original := revisionSnapshot(t, e).Goals[0]
	record, base, err := e.cfg.Records.Read(t.Context(), original.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	// A record edit or restore can commit before the engine's next reload.
	canonical := original
	canonical.ServiceRepository = "other"
	latest, err := e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: record.ID, Base: base, Path: record.Path, Data: definitionBytes(canonical)}, g.Person, g.Agent)
	if err != nil {
		t.Fatal(err)
	}
	d := definition(canonical)
	d.ServiceRepository = ""
	d.Outcome = "clarified current record"
	if _, err := e.Revise(t.Context(), g.ID, latest, g.Person, g.Agent, d); err != nil {
		t.Fatal(err)
	}
	if got := revisionSnapshot(t, e).Goals[0]; got.ServiceRepository != "other" {
		t.Fatalf("correction restored stale ledger binding %q", got.ServiceRepository)
	}
}
