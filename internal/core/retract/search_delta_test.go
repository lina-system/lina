// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestSearchRecoveryKeepsOtherHiddenAncestors(t *testing.T) {
	t.Parallel()
	db, removals, flow, _, _ := memoryFixture(t)
	first := seedMemory(t, db, flow, "first", "first-conversation", "도서관 first")
	second := seedMemory(t, db, flow, "second", "second-conversation", "도서관 second")
	child, err := deriveMemory(t, db, first.ID, "도서관 child")
	if err != nil {
		t.Fatal(err)
	}
	joined, err := deriveMemory(t, db, child.ID, "도서관 joined")
	if err != nil {
		t.Fatal(err)
	}
	// Give the joined item two independent ancestor paths before any removal.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE memory_item SET record=json_insert(record,
 '$.evidence[#]',json_object('kind','item','id',?),
 '$.evidence[#]',json_object('kind','item','id',?)) WHERE id=?`, second.ID, first.ID, joined.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	grandchild, err := deriveMemory(t, db, joined.ID, "도서관 grandchild")
	if err != nil {
		t.Fatal(err)
	}
	seedUnrelatedSearchItems(t, db, 1)
	countSearchWrites(t, db)
	request := func(id string) retract.Request {
		return retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: id}}}
	}
	for _, step := range []struct {
		id       string
		restore  bool
		inserted int
		deleted  int
		visible  int
	}{
		{first.ID, false, 0, 4, 2},
		{second.ID, false, 0, 1, 1},
		{first.ID, true, 2, 0, 3},
		{second.ID, true, 3, 0, 6},
	} {
		if step.restore {
			err = removals.Restore(t.Context(), request(step.id))
		} else {
			err = removals.Forget(t.Context(), request(step.id))
		}
		if err != nil {
			t.Fatal(err)
		}
		assertSearchWrites(t, db, step.inserted, step.deleted, step.visible)
	}
	if err := removals.Delete(t.Context(), request(first.ID)); err != nil {
		t.Fatal(err)
	}
	assertSearchWrites(t, db, 0, 4, 2)
	// The erased ancestor is now absent from memory_item. Late text and Hangul
	// postings must still be fenced through its retained removal target.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, id := range []string{child.ID, joined.ID, grandchild.ID} {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO memory_search_text(sequence,item_id,body)
 SELECT sequence,id,normalized FROM memory_item WHERE id=?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO memory_search_bigram(gram,item_id) VALUES('도서',?)", id); err != nil {
				return err
			}
		}
		var count int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_search_bigram WHERE item_id IN (?,?,?)`, child.ID, joined.ID, grandchild.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("late Hangul postings survived: %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertSearchWrites(t, db, 3, 0, 2)
}

func TestSearchRefreshDeduplicatesCyclicEvidence(t *testing.T) {
	t.Parallel()
	db, removals, flow, _, _ := memoryFixture(t)
	root := seedMemory(t, db, flow, "source", "conversation", "도서관 root")
	child, err := deriveMemory(t, db, root.ID, "도서관 child")
	if err != nil {
		t.Fatal(err)
	}
	// Imported or older data can contain cycles. Both graph walks must terminate
	// and touch each document once even when the root is reachable from itself.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE memory_item SET record=json_insert(record,
 '$.evidence[#]',json_object('kind','item','id',?)) WHERE id=?`, child.ID, root.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	countSearchWrites(t, db)
	request := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: root.ID}}}
	if err := removals.Forget(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertSearchWrites(t, db, 0, 2, 0)
	if err := removals.Restore(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertSearchWrites(t, db, 2, 0, 2)
}

func TestMemoryStateRefreshOnlyTouchesDependents(t *testing.T) {
	t.Parallel()
	db, _, flow, _, _ := memoryFixture(t)
	root := seedMemory(t, db, flow, "source", "conversation", "도서관 root")
	child, err := deriveMemory(t, db, root.ID, "도서관 child")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deriveMemory(t, db, child.ID, "도서관 grandchild"); err != nil {
		t.Fatal(err)
	}
	seedUnrelatedSearchItems(t, db, 3)
	countSearchWrites(t, db)
	for _, state := range []string{"retracted", "active"} {
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "UPDATE memory_item SET state=?,record=json_set(record,'$.state',?) WHERE id=?", state, state, root.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if state == "active" {
			assertSearchWrites(t, db, 3, 0, 6)
		} else {
			assertSearchWrites(t, db, 0, 3, 3)
		}
	}
}

func TestForgetWritesOnlyHiddenMemory(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 100, 400} {
		for _, derived := range []bool{false, true} {
			t.Run(fmt.Sprintf("active-%d/derived-%t", count, derived), func(t *testing.T) {
				t.Parallel()
				db, removals, flow, _, _ := memoryFixture(t)
				root := seedMemory(t, db, flow, "source", "conversation", "도서관 violet source")
				seedUnrelatedSearchItems(t, db, count-1)
				hidden := 1
				if derived {
					child, err := deriveMemory(t, db, root.ID, "도서관 violet child")
					if err != nil {
						t.Fatal(err)
					}
					if _, err := deriveMemory(t, db, child.ID, "도서관 violet grandchild"); err != nil {
						t.Fatal(err)
					}
					hidden = 3
				}
				countSearchWrites(t, db)
				request := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: root.ID}}}
				if err := removals.Forget(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				assertSearchWrites(t, db, 0, hidden, count-1)
				if err := removals.Restore(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				assertSearchWrites(t, db, hidden, 0, count-1+hidden)
				// The update trigger must handle a later forget as narrowly as insert.
				if err := removals.Forget(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				assertSearchWrites(t, db, 0, hidden, count-1)
			})
		}
	}
}

// Populate unrelated synthetic index documents in one transaction. The target
// and its descendants above use the real memory writer and removal coordinator.
func seedUnrelatedSearchItems(t *testing.T, db *canon.Store, count int) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for i := range count {
			id := fmt.Sprintf("unrelated-%d", i)
			_, err := tx.ExecContext(t.Context(), searchProbeItemSQL, id, "unrelated synthetic sentence")
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

const searchProbeItemSQL = `INSERT INTO memory_item
 (id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES(?,'person-1','agent-1','world','',?,'synthetic','local','active',0,0,'',0,'{"evidence":[]}')`

func countSearchWrites(t *testing.T, db *canon.Store) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		// BEFORE INSERT also counts ignored attempts: they still run the expensive
		// visibility fence. AFTER DELETE counts rows actually removed.
		_, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE search_writes(operation TEXT);
CREATE TEMP TRIGGER count_search_insert BEFORE INSERT ON main.memory_search_text BEGIN
 INSERT INTO search_writes VALUES('insert'); END;
CREATE TEMP TRIGGER count_search_delete AFTER DELETE ON main.memory_search_text BEGIN
 INSERT INTO search_writes VALUES('delete'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSearchWrites(t *testing.T, db *canon.Store, inserts, deletes, remaining int) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, check := range []struct {
			query string
			want  int
		}{
			{"SELECT count(*) FROM search_writes WHERE operation='insert'", inserts},
			{"SELECT count(*) FROM search_writes WHERE operation='delete'", deletes},
			{"SELECT count(*) FROM memory_search_text", remaining},
		} {
			var got int
			if err := tx.QueryRowContext(t.Context(), check.query).Scan(&got); err != nil {
				return err
			}
			if got != check.want {
				t.Errorf("%s: got %d, want %d", check.query, got, check.want)
			}
		}
		// Compare identities with the established visibility projection, so a
		// correct row count cannot conceal a different set of recalled items.
		var difference int
		if err := tx.QueryRowContext(t.Context(), `SELECT
 (SELECT count(*) FROM (SELECT item_id FROM memory_search_text EXCEPT SELECT id FROM retract_visible_memory WHERE state='active'))
 + (SELECT count(*) FROM (SELECT id FROM retract_visible_memory WHERE state='active' EXCEPT SELECT item_id FROM memory_search_text))`).Scan(&difference); err != nil {
			return err
		}
		if difference != 0 {
			t.Errorf("index differs from visible memory: %d", difference)
		}
		_, err := tx.ExecContext(t.Context(), "DELETE FROM search_writes")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
