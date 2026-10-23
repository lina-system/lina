// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"slices"
	"testing"
)

func TestNameIsCommonKnowledge(t *testing.T) {
	db := database(t)
	in := proposal("A", "The desk computer is Birch, formerly Cedar.")
	if err := json.Unmarshal([]byte(`{"kind":"name","subject":{"kind":"world","id":"desk-computer"},"name":{"current":"Birch","retired_aliases":["Cedar"]}}`), &in.Item); err != nil {
		t.Fatal(err)
	}
	item := commit(t, db, in)
	for _, agent := range []string{"A", "B", "C"} {
		hits, err := New(db).Recall(t.Context(), RecallScope{Agent: agent, Person: "person"}, Query{At: 100})
		if err != nil || len(hits) != 1 || hits[0].ID != item.ID || hits[0].Holder != All {
			t.Fatalf("name not common for %s: %+v %v", agent, hits, err)
		}
		raw, _ := json.Marshal(hits[0])
		var record struct {
			Name struct {
				Current string
				Retired []string `json:"retired_aliases"`
			}
		}
		if err := json.Unmarshal(raw, &record); err != nil || record.Name.Current != "Birch" || len(record.Name.Retired) != 1 || record.Name.Retired[0] != "Cedar" {
			t.Fatalf("name structure lost: %s %v", raw, err)
		}
	}
}

func TestNameCorrectionsRetainIdentityAndAliasesAcrossAgents(t *testing.T) {
	db := database(t)
	in := proposal("A", "The computer is Cedar.")
	in.Item.Kind = Name
	in.Item.Subject = Subject{Kind: World, ID: "computer"}
	in.Item.Name = &NameValue{Current: "Cedar"}
	first := commit(t, db, in)
	in = proposal("B", "The computer is Birch.")
	in.Item.Kind = Name
	in.Item.Subject = first.Subject
	in.Item.Name = &NameValue{Current: "Birch"}
	corrected := commit(t, db, in)
	if corrected.ID != first.ID || corrected.Revision != 2 || len(corrected.Name.RetiredAliases) != 1 || corrected.Name.RetiredAliases[0] != "Cedar" {
		t.Fatalf("correction: %+v", corrected)
	}
	repeated := commit(t, db, in)
	if repeated.ID != corrected.ID || repeated.Revision != corrected.Revision {
		t.Fatal("repeated correction minted another revision")
	}
	in = proposal("C", "The computer is Maple.")
	in.Item.Kind = Name
	in.Item.Subject = first.Subject
	in.Item.Name = &NameValue{Current: "Maple"}
	latest := commit(t, db, in)
	if latest.ID != first.ID || latest.Revision != 3 || !slices.Equal(latest.Name.RetiredAliases, []string{"Cedar", "Birch"}) {
		t.Fatalf("second correction: %+v", latest)
	}
	hits, err := New(db).Names(t.Context(), RecallScope{Agent: "D", Person: "person", ExternalModel: true}, 100)
	if err != nil || len(hits) != 1 || hits[0].ID != first.ID || hits[0].Name.Current != "Maple" {
		t.Fatalf("names: %+v %v", hits, err)
	}
	for _, e := range hits[0].Evidence {
		if e.Quote != "" {
			t.Fatal("foreign quote disclosed")
		}
	}
	var revisions, corrections int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_revision WHERE item_id=?`, first.ID).Scan(&revisions); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_item WHERE json_extract(record,'$.kind')='correction' AND EXISTS(SELECT 1 FROM json_each(record,'$.evidence') WHERE json_extract(value,'$.kind')='item' AND json_extract(value,'$.id')=?)`, first.ID).Scan(&corrections)
	}); err != nil || revisions != 3 || corrections != 2 {
		t.Fatalf("revision/correction evidence: %d/%d %v", revisions, corrections, err)
	}
}

func TestNameScopeAndCompleteSelection(t *testing.T) {
	db := database(t)
	add := func(subject string, change func(*Write)) Item {
		in := proposal("A", "The name is "+subject)
		in.Item.Kind = Name
		in.Item.Name = &NameValue{Current: subject}
		in.Item.Subject = Subject{Kind: World, ID: subject}
		change(&in)
		return commit(t, db, in)
	}
	for i := 0; i < 105; i++ {
		add(fmt.Sprintf("name-%03d", i), func(*Write) {})
	}
	add("foreign holder", func(in *Write) { in.Item.Holder = "A"; in.ExplicitHolder = true })
	add("private", func(in *Write) { in.Item.Scope = Private })
	add("never", func(in *Write) { in.Item.Scope = NeverLeaves })
	add("future", func(in *Write) { in.Item.ValidFrom = 101 })
	add("expired", func(in *Write) { in.Item.ValidUntil = 99 })
	// More recent ordinary items cannot displace names before the kind filter.
	for i := 0; i < 105; i++ {
		commit(t, db, proposal("A", fmt.Sprintf("ordinary-%d", i)))
	}
	for _, tc := range []struct {
		scope RecallScope
		want  int
	}{
		{RecallScope{Agent: "B", Person: "person", ExternalModel: true}, 106},
		{RecallScope{Agent: "B", Person: "person", Projection: true}, 105},
		{RecallScope{Agent: "B", Person: "another"}, 0},
	} {
		hits, err := New(db).Names(t.Context(), tc.scope, 100)
		if err != nil || len(hits) != tc.want {
			t.Fatalf("scope %+v got %d want %d: %v", tc.scope, len(hits), tc.want, err)
		}
	}
}

func TestInvalidNameValues(t *testing.T) {
	for _, value := range []*NameValue{nil, {Current: ""}, {Current: "Birch", RetiredAliases: []string{"birch"}}, {Current: "Birch", RetiredAliases: []string{"Cedar", "CEDAR"}}, {Current: "Birch\nextra"}} {
		in := proposal("A", "name")
		in.Item.Kind = Name
		in.Item.Subject = Subject{Kind: World, ID: "computer"}
		in.Item.Name = value
		if _, err := validate(in); err == nil {
			t.Fatalf("invalid name accepted: %+v", value)
		}
	}
}
