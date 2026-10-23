// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"testing"
)

func TestCitationMergeStoresOnlyNewEvidence(t *testing.T) {
	db := database(t)
	var item Item
	for i := 0; i < 200; i++ {
		in := proposal("A", "same statement")
		in.Item.Evidence[0].ID = fmt.Sprintf("A/event-%d", i)
		item = commit(t, db, in)
	}
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"memory_item", "memory_revision"} {
			var copied int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" r,json_each(r.record,'$.evidence') e WHERE json_extract(e.value,'$.quote') IS NOT NULL").Scan(&copied); err != nil {
				return err
			}
			if copied != 0 {
				t.Errorf("%s retained %d event quotes after 200 merges", table, copied)
			}
		}
		var rows int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM cite WHERE view_kind='memory' AND view_id=?", item.ID).Scan(&rows); err != nil {
			return err
		}
		if rows != 200 {
			t.Errorf("200 merges wrote %d citation rows", rows)
		}
		for rev := 1; rev <= 200; rev++ {
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM cite WHERE view_id=? AND CAST(added_revision AS INTEGER)<=? AND (dropped_revision IS NULL OR CAST(dropped_revision AS INTEGER)>?)", item.ID, rev, rev).Scan(&rows); err != nil {
				return err
			}
			if rows != rev {
				t.Fatalf("revision %d has %d citations", rev, rows)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryCitations(t, db)
}

// Every historical pointer must be represented once at its revision, and every
// citation interval must correspond to at least one retained revision pointer.
func assertMemoryCitations(t *testing.T, db *canon.Store) {
	t.Helper()
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var missing, extra int
		pointer := `SELECT r.item_id,r.revision,e.key AS ordinal,
 CASE json_extract(e.value,'$.kind') WHEN 'item' THEN 'memory' ELSE json_extract(e.value,'$.kind') END AS kind,
 json_extract(e.value,'$.id') AS id,CAST(coalesce(json_extract(e.value,'$.revision'),0) AS TEXT) AS target_revision
 FROM memory_revision r,json_each(r.record,'$.evidence') e`
		actual := `SELECT c.view_id,r.revision,c.ordinal,c.target_kind,c.target_id,c.target_revision
 FROM cite c JOIN memory_revision r ON r.item_id=c.view_id AND r.revision>=CAST(c.added_revision AS INTEGER)
 AND (c.dropped_revision IS NULL OR r.revision<CAST(c.dropped_revision AS INTEGER)) WHERE c.view_kind='memory'`
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ("+pointer+" EXCEPT "+actual+")").Scan(&missing); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ("+actual+" EXCEPT "+pointer+")").Scan(&extra); err != nil {
			return err
		}
		if missing != 0 || extra != 0 {
			t.Errorf("memory citation parity: missing=%d extra=%d", missing, extra)
		}
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM cite c WHERE view_kind='memory' AND NOT EXISTS(SELECT 1 FROM memory_revision r WHERE r.item_id=c.view_id AND r.revision=CAST(c.added_revision AS INTEGER))`).Scan(&extra); err != nil {
			return err
		}
		if extra != 0 {
			t.Errorf("orphan citation intervals=%d", extra)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCitationFailureRollsBackMemoryAndRevision(t *testing.T) {
	db := database(t)
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_cite BEFORE INSERT ON cite BEGIN SELECT RAISE(ABORT,'injected_citation_failure'); END;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := Insert(t.Context(), tx, proposal("A", "atomic memory"), personOrigin)
		return err
	})
	if err == nil {
		t.Fatal("citation failure was ignored")
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"cite", "memory_item", "memory_revision"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("partial write in %s: %d", table, n)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCitationBriefGenerationRetiresPreviousSentences(t *testing.T) {
	db := jobDatabase(t)
	store := New(db)
	scope := RecallScope{Agent: "A", Person: "person"}
	item := commit(t, db, proposal("A", "tea preference"))
	for generation := int64(1); generation <= 2; generation++ {
		task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", generation*10, 2, 100, nil, personOrigin)
		if err != nil || !ready {
			t.Fatalf("prepare: %t %v", ready, err)
		}
		refs := []Evidence{{Kind: MemoryItem, ID: item.ID, Revision: item.Revision}}
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			return PublishBrief(t.Context(), tx, task, []BriefSentence{{Text: "One observation.", Citations: refs}, {Text: "Another observation.", Citations: refs}}, 100, personOrigin)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var total, current, retired int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*),count(CASE WHEN dropped_revision IS NULL THEN 1 END),count(CASE WHEN added_revision='1' AND dropped_revision='2' THEN 1 END) FROM cite WHERE view_kind='person_brief'`).Scan(&total, &current, &retired); err != nil {
			return err
		}
		if total != 4 || current != 2 || retired != 2 {
			t.Errorf("brief generations total=%d current=%d retired=%d", total, current, retired)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
