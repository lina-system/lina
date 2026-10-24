// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRelationshipInsertClosesPredecessorInterval(t *testing.T) {
	t.Parallel()
	db := database(t)
	in := proposal("A", "We are study partners.")
	in.Item.Kind = Relationship
	in.Item.ValidFrom = 10
	first := commit(t, db, in)
	in = proposal("A", "We are colleagues.")
	in.Item.Kind = Relationship
	in.Item.ValidFrom = 20
	in.Item.Supersedes = first.ID
	second := commit(t, db, in)
	// Ordinary Insert must persist non-overlapping intervals, even when a
	// reader does not follow the replacement's supersedes link.
	var valid int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_item
			WHERE person='person' AND holder='A' AND state='active'
			AND json_extract(record,'$.kind')='relationship'
			AND valid_from<=20 AND (valid_until=0 OR valid_until>20)`).Scan(&valid)
	}); err != nil {
		t.Fatal(err)
	}
	if valid != 1 {
		t.Fatalf("non-tool writer left %d valid relationship views at replacement time; want 1", valid)
	}
	hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{At: 20, History: true})
	if err != nil || len(hits) != 2 {
		t.Fatalf("relationship history: %+v %v", hits, err)
	}
	for _, hit := range hits {
		switch hit.ID {
		case first.ID:
			if !hit.Older || hit.ValidUntil != 20 || hit.Revision != 2 {
				t.Fatalf("predecessor interval not persisted: %+v", hit)
			}
		case second.ID:
			if hit.Older || hit.ValidFrom != 20 || hit.Revision != 1 {
				t.Fatalf("replacement interval: %+v", hit)
			}
		default:
			t.Fatalf("unexpected relationship: %+v", hit)
		}
	}
}

func TestRelationshipReplacementRejectsMissingOrEarlierTime(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		at   int64
		want error
	}{
		{name: "missing", want: ErrInvalid},
		{name: "earlier", at: 9, want: ErrSupersedes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := database(t)
			in := proposal("A", "We are study partners.")
			in.Item.Kind = Relationship
			in.Item.ValidFrom = 10
			first := commit(t, db, in)
			in = proposal("A", "We are colleagues.")
			in.Item.Kind = Relationship
			in.Item.ValidFrom = tc.at
			in.Item.Supersedes = first.ID
			err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := Insert(t.Context(), tx, in, personOrigin)
				return err
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("replacement error: got %v, want %v", err, tc.want)
			}
			hits, err := New(db).CurrentRelationship(t.Context(), RecallScope{Agent: "A", Person: "person"}, 100)
			if err != nil || len(hits) != 1 || hits[0].ID != first.ID || hits[0].Revision != 1 || hits[0].ValidUntil != 0 {
				t.Fatalf("rejected replacement changed predecessor: %+v %v", hits, err)
			}
		})
	}
}

func TestRelationshipReplacementRollbackPreservesPredecessor(t *testing.T) {
	t.Parallel()
	db := database(t)
	in := proposal("A", "We are study partners.")
	in.Item.Kind = Relationship
	in.Item.ValidFrom = 10
	first := commit(t, db, in)
	in = proposal("A", "We are colleagues.")
	in.Item.Kind = Relationship
	in.Item.ValidFrom = 20
	in.Item.Supersedes = first.ID
	abort := errors.New("abort replacement transaction")
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := Insert(t.Context(), tx, in, personOrigin); err != nil {
			return err
		}
		previous, err := readItem(t.Context(), tx, first.ID)
		if err != nil {
			return err
		}
		if previous.ValidUntil != 20 || previous.Revision != 2 {
			t.Errorf("predecessor not closed inside replacement transaction: %+v", previous)
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("replacement rollback: %v", err)
	}
	hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{At: 100, History: true})
	if err != nil || len(hits) != 1 || hits[0].ID != first.ID || hits[0].Revision != 1 || hits[0].ValidUntil != 0 || hits[0].Older {
		t.Fatalf("rolled-back replacement changed predecessor: %+v %v", hits, err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var revisions int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_revision WHERE item_id=?", first.ID).Scan(&revisions); err != nil {
			return err
		}
		if revisions != 1 {
			t.Errorf("rolled-back replacement left %d predecessor revisions; want 1", revisions)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRelationshipRecallKeepsObserverAndPersonBeforeLimit(t *testing.T) {
	t.Parallel()
	db := database(t)
	s := New(db)
	in := proposal("A", "study partners")
	in.Item.Kind = Relationship
	first := commit(t, db, in)
	in = proposal("B", "other agent view")
	in.Item.Kind = Relationship
	commit(t, db, in)
	// Many newer ordinary memories cannot displace a relationship before filtering.
	for i := 0; i < 110; i++ {
		in = proposal("A", string(rune(0x4e00+i)))
		commit(t, db, in)
	}
	hits, err := s.CurrentRelationship(t.Context(), RecallScope{Agent: "A", Person: "person", ExternalModel: true}, 100)
	if err != nil || len(hits) != 1 || hits[0].ID != first.ID {
		t.Fatalf("relationship candidate: %+v %v", hits, err)
	}
	for _, scope := range []RecallScope{{Agent: "C", Person: "person"}, {Agent: "A", Person: "other"}, {Agent: "A", Person: "person", Projection: true}} {
		hits, err := s.CurrentRelationship(t.Context(), scope, 100)
		if err != nil || len(hits) != 0 {
			t.Fatalf("relationship escaped observer pair: %+v %v", hits, err)
		}
	}
	in = proposal("A", "new shared experience")
	in.Item.Kind = Relationship
	in.Item.Supersedes = first.ID
	in.Item.ValidFrom = 10
	second := commit(t, db, in)
	hits, err = s.CurrentRelationship(t.Context(), RecallScope{Agent: "A", Person: "person"}, 100)
	if err != nil || len(hits) != 1 || hits[0].ID != second.ID {
		t.Fatalf("old relationship remained current: %+v %v", hits, err)
	}
}
