// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestPersonBriefCitationsTopicAgeAndSupersededJobs(t *testing.T) {
	db := jobDatabase(t)
	store := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	in := proposal("A", "prefers tea")
	in.Item.Standing = true
	item := commit(t, db, in)
	prepare := func(turn int64) BriefTask {
		t.Helper()
		task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", turn, 2, 100, nil, personOrigin)
		if err != nil || !ready {
			t.Fatalf("prepare: %+v %t %v", task, ready, err)
		}
		return task
	}
	publish := func(task BriefTask, sentences []BriefSentence) error {
		return db.Write(t.Context(), func(tx *canon.Tx) error { return PublishBrief(t.Context(), tx, task, sentences, 100, personOrigin) })
	}
	first := prepare(1)
	newer := prepare(2)
	sentence := BriefSentence{Text: "Tea matters to the person.", Citations: []Evidence{{Kind: MemoryItem, ID: item.ID, Revision: item.Revision}}}
	if err := publish(first, []BriefSentence{sentence}); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("superseded job: %v", err)
	}
	if err := publish(newer, []BriefSentence{sentence, {Text: "uncited assertion"}, {Text: "fabricated source", Citations: []Evidence{{Kind: MemoryItem, ID: "missing", Revision: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var count int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM cite WHERE view_kind='person_brief' AND view_id=? AND added_revision=? AND target_kind='memory' AND target_id=? AND target_revision=? AND dropped_revision IS NULL", canon.BriefCitationID(scope.Person, scope.Agent), newer.Generation, item.ID, item.Revision).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("published brief citations=%d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.PersonBrief(t.Context(), scope, "tea", 6, 100, personOrigin)
	if err != nil || len(got) != 1 || len(got[0].Citations) != 1 {
		t.Fatalf("brief: %+v %v", got, err)
	}
	got, err = store.PersonBrief(t.Context(), scope, "coffee", 3, 100, personOrigin)
	if err != nil || len(got) != 0 {
		t.Fatalf("wrong topic: %+v %v", got, err)
	}
	got, err = store.PersonBrief(t.Context(), scope, "tea", 7, 100, personOrigin)
	if err != nil || len(got) != 0 {
		t.Fatalf("old result: %+v %v", got, err)
	}
	// A revision change invalidates the result even without a revocation bump.
	in.Agent = "B"
	in.Item.Evidence[0].ID = "B/tea"
	commit(t, db, in)
	got, err = store.PersonBrief(t.Context(), scope, "tea", 3, 100, personOrigin)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale evidence: %+v %v", got, err)
	}
}

func TestBriefEmptyBackoffAndRevocation(t *testing.T) {
	db := jobDatabase(t)
	store := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	commit(t, db, proposal("A", "tea preference"))
	task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", 1, 2, 100, nil, personOrigin)
	if err != nil || !ready {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return PublishBrief(t.Context(), tx, task, nil, 100, personOrigin) }); err != nil {
		t.Fatal(err)
	}
	_, ready, err = store.PrepareBrief(t.Context(), scope, "tea", 2, 2, 100, nil, personOrigin)
	if err != nil || ready {
		t.Fatalf("no backoff: %t %v", ready, err)
	}
	task, ready, err = store.PrepareBrief(t.Context(), scope, "tea", 4, 2, 100, nil, personOrigin)
	if err != nil || !ready {
		t.Fatalf("backoff retry: %t %v", ready, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE retract_meta SET epoch=epoch+1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return PublishBrief(t.Context(), tx, task, []BriefSentence{{Text: "old fact", Citations: []Evidence{task.Sources[0].Evidence}}}, 100, personOrigin)
	}); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("revoked input: %v", err)
	}
}

func TestBriefNeverLeavesAndCrossAgentScope(t *testing.T) {
	db := jobDatabase(t)
	store := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	hidden := proposal("A", "tea private source")
	hidden.Item.Scope = NeverLeaves
	commit(t, db, hidden)
	foreign := proposal("B", "tea personal source")
	foreign.Item.Holder = "B"
	foreign.ExplicitHolder = true
	commit(t, db, foreign)
	task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", 1, 2, 100, nil, personOrigin)
	if err != nil || ready || len(task.Sources) != 0 {
		t.Fatalf("scope leak: %+v %t %v", task, ready, err)
	}
}

func TestBriefDispatchRechecksPreparedEvidence(t *testing.T) {
	db := jobDatabase(t)
	store := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	in := proposal("A", "tea preference")
	commit(t, db, in)
	task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", 1, 2, 100, nil, personOrigin)
	if err != nil || !ready {
		t.Fatal(err)
	}
	if err := store.CheckBrief(t.Context(), task, 100, personOrigin); err != nil {
		t.Fatal(err)
	}
	in.Agent = "B"
	in.Item.Evidence[0].ID = "B/tea"
	commit(t, db, in)
	if err := store.CheckBrief(t.Context(), task, 100, personOrigin); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("stale dispatch: %v", err)
	}
}
