// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestRelationshipReplacementRejectsMissingOrEarlierTime(t *testing.T) {
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

func TestRelationshipRecallKeepsObserverAndPersonBeforeLimit(t *testing.T) {
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
