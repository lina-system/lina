// SPDX-License-Identifier: Apache-2.0
package memory

import "testing"

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
	second := commit(t, db, in)
	hits, err = s.CurrentRelationship(t.Context(), RecallScope{Agent: "A", Person: "person"}, 100)
	if err != nil || len(hits) != 1 || hits[0].ID != second.ID {
		t.Fatalf("old relationship remained current: %+v %v", hits, err)
	}
}
