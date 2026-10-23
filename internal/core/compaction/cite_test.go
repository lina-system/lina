// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"testing"
)

func TestCitationPublicationRebuildAndRollback(t *testing.T) {
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	publish(t, s, Proposal{ID: "root", Expected: frontier(t, s), Children: []string{"leaf"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("parent")})
	check := func() {
		t.Helper()
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var missing, extra int
			old := `SELECT n.id,'event' AS kind,c.event_id AS target,'0' AS rev,c.ordinal+(SELECT count(*) FROM sum_edge e WHERE e.parent_id=n.id) AS ordinal,CASE reason WHEN '' THEN 'covers' ELSE reason END AS role FROM sum_cover c JOIN sum_node n ON n.id=c.node_id
 UNION ALL SELECT e.parent_id,'summary',e.child_id,CAST(n.revision AS TEXT),e.ordinal,'cites' FROM sum_edge e JOIN sum_node n ON n.id=e.child_id`
			old = "SELECT * FROM (" + old + ")"
			current := `SELECT view_id,target_kind,target_id,target_revision,ordinal,role FROM cite WHERE view_kind='summary' AND dropped_revision IS NULL`
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ("+old+" EXCEPT "+current+")").Scan(&missing); err != nil {
				return err
			}
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ("+current+" EXCEPT "+old+")").Scan(&extra); err != nil {
				return err
			}
			if missing != 0 || extra != 0 {
				t.Errorf("summary citation parity missing=%d extra=%d", missing, extra)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check()
	receipt, err := memoryexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Fact, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Content: "source fact", Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "e1", Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	req := retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}
	if err := r.Forget(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	rebuild := func() {
		t.Helper()
		if _, err := s.Rebuild(t.Context(), "conversation", func(context.Context, Segment) (Output, error) { return output("rebuilt"), nil }); err != nil {
			t.Fatal(err)
		}
		check()
	}
	rebuild()
	if err := r.Restore(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	rebuild()
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var role, child string
		if err := tx.QueryRowContext(t.Context(), "SELECT role FROM cite WHERE view_id='leaf' AND added_revision='1' AND dropped_revision='2'").Scan(&role); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT target_revision FROM cite WHERE view_id='root' AND target_kind='summary' AND added_revision='1' AND dropped_revision='2'").Scan(&child); err != nil {
			return err
		}
		if role != "covers" || child != "1" {
			t.Errorf("historical provenance: role=%s child=%s", role, child)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	addEvent(t, db, "e3", "conversation")
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_cite BEFORE INSERT ON cite BEGIN SELECT RAISE(ABORT,'injected_citation_failure'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(t.Context(), Proposal{ID: "failed", Expected: frontier(t, s), Children: []string{"root"}, Cover: []Coverage{{EventID: "e3"}}, Output: output("failed")})
	if err == nil {
		t.Fatal("ignored citation failure")
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sum_node WHERE id='failed'").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("partial summary publication")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check()
}
