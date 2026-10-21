// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
)

func database(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func proposal(agent, content string) Write {
	return Write{Agent: agent, Person: "person", Item: Item{Kind: Fact, Content: content, Subject: Subject{Person, "person"}, Basis: Stated, Evidence: []Evidence{{Kind: Event, ID: agent + "/" + content, Precision: Exact}}}}
}
func personOrigin(_ context.Context, _ *canon.Tx, e Evidence) (Origin, error) {
	return Origin{Person: "person", Agent: strings.SplitN(e.ID, "/", 2)[0], Speaker: guard.Person, Quote: "original statement", Scope: Local}, nil
}
func commit(t *testing.T, db *canon.Store, in Write) Item {
	t.Helper()
	var item Item
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		item, err = Insert(t.Context(), tx, in, personOrigin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return item
}
func TestRecallFiltersBeforeLimitAndHidesForeignEvidence(t *testing.T) {
	db := database(t)
	store := New(db)
	a := proposal("A", "shared fact")
	common := commit(t, db, a)
	// Same common fact, learned through B, has one id with two evidence refs.
	b := proposal("B", "  SHARED   fact ")
	duplicate := commit(t, db, b)
	if duplicate.ID != common.ID || len(duplicate.Evidence) != 2 || duplicate.Revision != 2 {
		t.Fatalf("fan-out duplicated: %+v", duplicate)
	}
	private := proposal("B", "shared fact with a stronger text match")
	private.Item.Holder = "B"
	private.ExplicitHolder = true
	hidden := commit(t, db, private)
	other := proposal("A", "other person")
	other.Person = "other"
	other.Item.Subject = Subject{World, "world"}
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := Insert(t.Context(), tx, other, func(context.Context, *canon.Tx, Evidence) (Origin, error) {
			return Origin{Person: "other", Agent: "A", Speaker: guard.Person, Scope: Local}, nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := store.Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{Text: "shared", At: 100, Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].ID != common.ID {
		t.Fatalf("scope leaked/ranked incorrectly: %+v %v", hits, err)
	}
	if hits[0].ID == hidden.ID {
		t.Fatal("B's private item was a candidate")
	}
	for _, e := range hits[0].Evidence {
		if e.SourceAgent == "B" && e.Quote != "" {
			t.Fatal("foreign conversation body disclosed")
		}
		if e.SourceAgent == "A" && e.Quote == "" {
			t.Fatal("own source quote lost")
		}
	}
}
func TestPersonSubjectChecksCanonicalSpeakerAndOwner(t *testing.T) {
	for _, speaker := range []guard.SpeakerKind{guard.Agent, guard.ExternalParticipant, guard.Automation, guard.Forwarded, guard.System, guard.Tool, ""} {
		t.Run(string(speaker), func(t *testing.T) {
			db := database(t)
			in := proposal("A", "claim")
			in.Item.Evidence[0].Quote = "the person says this"
			err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := Insert(t.Context(), tx, in, func(context.Context, *canon.Tx, Evidence) (Origin, error) {
					return Origin{Person: "person", Agent: "A", Speaker: speaker, Scope: Local}, nil
				})
				return err
			})
			if !errors.Is(err, ErrPersonEvidence) {
				t.Fatalf("speaker %q: %v", speaker, err)
			}
			hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{At: 100})
			if err != nil || len(hits) != 0 {
				t.Fatalf("failed write visible: %+v %v", hits, err)
			}
		})
	}
	db := database(t)
	for _, origin := range []Origin{{Person: "other", Agent: "A", Speaker: guard.Person, Scope: Local}, {Person: "person", Agent: "B", Speaker: guard.Person, Scope: Local}} {
		err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := Insert(t.Context(), tx, proposal("A", "claim"), func(context.Context, *canon.Tx, Evidence) (Origin, error) { return origin, nil })
			return err
		})
		if !errors.Is(err, ErrEvidence) {
			t.Fatalf("unrelated source: %v", err)
		}
	}
}
func TestMemoryValiditySupersessionAndProfile(t *testing.T) {
	db := database(t)
	s := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	first := proposal("A", "old fact")
	first.Item.Standing = true
	old := commit(t, db, first)
	next := proposal("A", "new fact")
	next.Item.Supersedes = old.ID
	next.Item.Standing = true
	newest := commit(t, db, next)
	expired := proposal("A", "dated fact")
	expired.Item.ValidFrom = 1
	expired.Item.ValidUntil = 50
	commit(t, db, expired)
	future := proposal("A", "future fact")
	future.Item.ValidFrom = 200
	commit(t, db, future)
	sensitive := proposal("A", "private fact")
	sensitive.Item.Scope = Private
	sensitive.Item.Standing = true
	secret := commit(t, db, sensitive)
	hits, err := s.Recall(t.Context(), scope, Query{At: 100})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	if len(ids) != 2 || !slices.Contains(ids, newest.ID) || !slices.Contains(ids, secret.ID) {
		t.Fatalf("invalid current set: %+v", hits)
	}
	scope.Projection = true
	hits, err = s.Recall(t.Context(), scope, Query{At: 100, History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("history scope: %+v", hits)
	}
	for _, h := range hits {
		if h.ID == old.ID && !h.Older {
			t.Fatal("superseded item not marked older")
		}
	}
	profile, err := s.Profile(t.Context(), scope, 100, 2, 1, func(string) int { return 1 })
	if err != nil || len(profile) != 1 || profile[0].ID != newest.ID {
		t.Fatalf("profile: %+v %v", profile, err)
	}
}
func TestWriteChecksAndDefaultLayers(t *testing.T) {
	db := database(t)
	for _, kind := range []Kind{SharedHistory, Relationship, EmotionObservation} {
		in := proposal("A", string(kind))
		in.Item.Kind = kind
		if got := commit(t, db, in); got.Holder != "A" {
			t.Fatalf("personal kind %s: %s", kind, got.Holder)
		}
	}
	in := proposal("A", "third party fact")
	in.Item.Subject = Subject{ThirdParty, "someone"}
	got := commit(t, db, in)
	if got.Holder != "A" || got.Scope != Private || !slices.Contains(got.Sensitivity, "third_party") {
		t.Fatalf("third party scope: %+v", got)
	}
	for _, change := range []func(*Write){
		func(w *Write) { w.Item.Holder = "B" },
		func(w *Write) { w.Item.Evidence = nil },
		func(w *Write) { w.Item.ValidUntil = 1; w.Item.ValidFrom = 2 },
		func(w *Write) { w.Item.Basis = Derived; w.Item.Supersedes = got.ID },
	} {
		w := proposal("A", "invalid")
		change(&w)
		err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := Insert(t.Context(), tx, w, personOrigin); return err })
		if err == nil {
			t.Fatalf("accepted %+v", w)
		}
	}
}

func TestProfileUsesLatestRevisionAndHasNoPersistentCopy(t *testing.T) {
	db := database(t)
	s := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	a := proposal("A", "older item")
	a.Item.Standing = true
	old := commit(t, db, a)
	b := proposal("A", "newer item")
	b.Item.Standing = true
	commit(t, db, b)
	a.Agent = "B"
	a.Item.Evidence[0].ID = "B"
	commit(t, db, a)
	profile, err := s.Profile(t.Context(), scope, 100, 1, 10, func(string) int { return 1 })
	if err != nil || len(profile) != 1 || profile[0].ID != old.ID || profile[0].Revision != 2 {
		t.Fatalf("latest revision was not first: %+v %v", profile, err)
	}
}
func TestWriteDoesNotRetainSecretShapedContent(t *testing.T) {
	db := database(t)
	in := proposal("A", "api_key=synthetic-secret-value")
	err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := Insert(t.Context(), tx, in, personOrigin); return err })
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret-shaped statement accepted: %v", err)
	}
}
