// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestKoreanIndexedRecall(t *testing.T) {
	var corpus struct {
		Documents []struct{ ID, Body string }
		Queries   []struct {
			Name, Text string
			IDs        []string
		}
	}
	data, err := os.ReadFile("../../../testdata/korean/search.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	db := database(t)
	s := New(db)
	fixtureIDs := map[string]string{}
	for _, doc := range corpus.Documents {
		item := commit(t, db, proposal("A", doc.Body))
		fixtureIDs[item.ID] = doc.ID
	}
	for _, query := range corpus.Queries {
		t.Run(query.Name, func(t *testing.T) {
			hits, err := s.Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{Text: query.Text})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, hit := range hits {
				ids = append(ids, fixtureIDs[hit.ID])
			}
			sort.Strings(ids)
			if !reflect.DeepEqual(ids, query.IDs) {
				t.Fatalf("ids=%v, want %v", ids, query.IDs)
			}
		})
	}
	// Establish the required storage mechanism too: literal scanning already
	// found these words before #65, but there were no searchable FTS/posting rows.
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var trigrams, bigrams int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search WHERE memory_search MATCH ?", canon.TrigramPhrase("도서관")).Scan(&trigrams); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search_bigram WHERE gram='공부'").Scan(&bigrams); err != nil {
			return err
		}
		if trigrams != 3 || bigrams != 2 {
			t.Fatalf("indexed counts: trigram=%d bigram=%d", trigrams, bigrams)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKoreanRecallKeepsScopeValidityAndHistory(t *testing.T) {
	db := database(t)
	s := New(db)
	current := commit(t, db, proposal("A", "도서관에서 공부를 한다."))
	private := proposal("B", "도서관에서 공부를 많이 한다.")
	private.Item.Holder, private.ExplicitHolder = "B", true
	commit(t, db, private)
	expired := proposal("A", "도서관에서 공부를 했었다.")
	expired.Item.ValidFrom, expired.Item.ValidUntil = 1, 50
	old := commit(t, db, expired)
	future := proposal("A", "도서관에서 공부를 할 것이다.")
	future.Item.ValidFrom = 200
	commit(t, db, future)
	scope := RecallScope{Agent: "A", Person: "person"}
	for _, text := range []string{"도서관", "공부"} {
		hits, err := s.Recall(t.Context(), scope, Query{Text: text, At: 100, Limit: 1})
		if err != nil || len(hits) != 1 || hits[0].ID != current.ID || hits[0].Older {
			t.Fatalf("current scope: %v %v", hits, err)
		}
		hits, err = s.Recall(t.Context(), scope, Query{Text: text, At: 100, History: true})
		if err != nil || len(hits) != 2 || hits[0].ID != old.ID || !hits[0].Older || hits[1].ID != current.ID {
			t.Fatalf("history scope: %v %v", hits, err)
		}
	}
}

func TestKoreanIndexRollsBackWithMemory(t *testing.T) {
	db := database(t)
	failure := errors.New("rollback")
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := Insert(t.Context(), tx, proposal("A", "도서관에서 공부한다."), personOrigin); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"memory_item", "memory_search", "memory_search_bigram"} {
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				t.Fatalf("rollback retained %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndexedRecallPreservesLiteralNormalizationAndShortQueries(t *testing.T) {
	db := database(t)
	item := commit(t, db, proposal("A", "Mixed  CASE\n도서관에서 공부를 한다. a% xy"))
	for _, text := range []string{"CASE 도서관", "mIxEd", "a%", "xy", "a", "도", "", "공부"} {
		hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{Text: text})
		if err != nil || len(hits) != 1 || hits[0].ID != item.ID {
			t.Fatalf("literal %q: %v %v", text, hits, err)
		}
	}
}

func TestMemoryIndexAndSourceBecomeVisibleTogether(t *testing.T) {
	db := database(t)
	var item Item
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		item, err = Insert(t.Context(), tx, proposal("A", "도서관에서 공부한다."), personOrigin)
		if err != nil {
			return err
		}
		for _, text := range []string{"도서관", "공부"} {
			hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{Text: text})
			if err != nil || len(hits) != 0 {
				t.Fatalf("uncommitted memory: %v %v", hits, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"도서관", "공부"} {
		hits, err := New(db).Recall(t.Context(), RecallScope{Agent: "A", Person: "person"}, Query{Text: text})
		if err != nil || len(hits) != 1 || hits[0].ID != item.ID {
			t.Fatalf("committed memory: %v %v", hits, err)
		}
	}
}
