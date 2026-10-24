// SPDX-License-Identifier: Apache-2.0
package recall_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

func write(t *testing.T, db *canon.Store, query string, args ...any) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { _, e := tx.ExecContext(t.Context(), query, args...); return e }); err != nil {
		t.Fatal(err)
	}
}
func drain(t *testing.T, db *canon.Store) {
	t.Helper()
	s := recall.New(db, nil)
	for count(t, db, "SELECT count(*) FROM recall_pending") > 0 {
		if _, err := s.DrainBatch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

const eventSQL = `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,'conversation','owner','agent','person','{"kind":"tui"}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','recorded','{"kind":"import"}',?)`

func TestDrainAllKindsAndExclusions(t *testing.T) {
	db := database(t)
	write(t, db, eventSQL, "imported", "도서관 비밀")
	write(t, db, "INSERT INTO ev_span VALUES('imported',?,?, 'secret')", len("도서관 "), len("도서관 비밀"))
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,replay_of,body)
 SELECT 'copy',conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,id,'도서관' FROM conv_event WHERE id='imported'`)
	write(t, db, `INSERT INTO sum_node VALUES('summary','conversation',0,'fixture','1','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z',0,'ready','도서관 요약','glossneedle',1)`)
	write(t, db, `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES('memory','owner','all','person','owner','도서관 기억','도서관 기억','local','active',1700000000,0,'',0,'{"revision":1,"evidence":[{"kind":"external","id":"fixture","quote":"quoteneedle"}],"gloss":"glossneedle"}')`)
	write(t, db, `INSERT INTO records_index VALUES('record','inbox/record.md','blob','revision'); INSERT INTO records_text VALUES('record','owner','memo','active','도서관 기록','본문');
 INSERT INTO import_record VALUES('observation','conversation','agent','observation',3,'unpromotedneedle','{}',NULL);
 INSERT INTO import_record VALUES('old-summary','conversation','agent','summary',3,'unpromotedneedle','{}',NULL)`)
	drain(t, db)
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 5 {
		t.Fatalf("documents=%d", n)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_text WHERE recall_text MATCH '도서관'"); n != 5 {
		t.Fatalf("searchable=%d", n)
	}
	for _, term := range []string{"비밀", "quoteneedle", "glossneedle", "unpromotedneedle"} {
		for _, table := range []string{"recall_text", "recall_pair"} {
			if n := count(t, db, "SELECT count(*) FROM "+table+" WHERE "+table+" MATCH ?", canon.TrigramPhrase(term)); n != 0 {
				t.Fatalf("indexed excluded term %s", term)
			}
		}
	}
	if n := count(t, db, "SELECT count(*) FROM recall_doc WHERE source_id='copy' AND replay=1"); n != 1 {
		t.Fatal("copy flag missing")
	}
	write(t, db, `UPDATE memory_item SET content='새로운 기억',normalized='새로운 기억',record='{"revision":2}' WHERE id='memory'; UPDATE sum_node SET status='invalid' WHERE id='summary'; DELETE FROM records_text WHERE id='record'`)
	if n := count(t, db, "SELECT count(*) FROM recall_doc WHERE kind<>'event'"); n != 0 {
		t.Fatal("stale documents survived changes")
	}
	drain(t, db)
	if n := count(t, db, "SELECT count(*) FROM recall_doc WHERE kind='memory' AND revision='2'"); n != 1 {
		t.Fatal("memory revision not advanced")
	}
	if n := count(t, db, "SELECT count(*) FROM recall_text WHERE recall_text MATCH '도서관'"); n != 2 {
		t.Fatalf("stale postings: %d", n)
	}
	write(t, db, "UPDATE memory_item SET state='trashed' WHERE id='memory'")
	drain(t, db)
	if n := count(t, db, "SELECT count(*) FROM recall_doc WHERE kind='memory'"); n != 0 {
		t.Fatal("trashed memory indexed")
	}
	write(t, db, "UPDATE memory_item SET state='active' WHERE id='memory'")
	drain(t, db)
	if n := count(t, db, "SELECT count(*) FROM recall_doc WHERE kind='memory'"); n != 1 {
		t.Fatal("restored memory missing")
	}
}
func TestDrainBatchLimitAndRetry(t *testing.T) {
	db := database(t)
	for i := 0; i < 129; i++ {
		write(t, db, eventSQL, fmt.Sprintf("event-%03d", i), "도서관")
	}
	s := recall.New(db, nil)
	if n, err := s.DrainBatch(t.Context()); err != nil || n != 128 {
		t.Fatalf("batch: %d %v", n, err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_pending"); n != 1 {
		t.Fatalf("remaining=%d", n)
	}
	drain(t, db)
	if n, err := s.DrainBatch(t.Context()); err != nil || n != 0 {
		t.Fatalf("repeat: %d %v", n, err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 129 {
		t.Fatalf("documents=%d", n)
	}
}
func TestMaterialReadOutsideTransactionAndStaleDrain(t *testing.T) {
	db := database(t)
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,material_id,material_revision)
 VALUES('material','conversation','owner','agent','person','{}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','observed','{"kind":"native"}','asset',1)`)
	if _, err := recall.New(db, nil).DrainBatch(t.Context()); !errors.Is(err, recall.ErrBytesUnavailable) {
		t.Fatalf("missing bytes: %v", err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_pending"); n != 1 {
		t.Fatal("failure lost pending row")
	}
	s := recall.New(db, func(ctx context.Context, id string) (string, error) {
		// A writer here would deadlock if material I/O held canon's read lock.
		err := db.Write(ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO ev_span VALUES('material',0,6,'secret')")
			return err
		})
		return "비밀 도서관", err
	})
	if n, err := s.DrainBatch(t.Context()); err != nil || n != 0 {
		t.Fatalf("stale batch: %d %v", n, err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 0 {
		t.Fatal("stale text published")
	}
	s = recall.New(db, func(context.Context, string) (string, error) { return " 도서관", nil })
	if n, err := s.DrainBatch(t.Context()); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_pair WHERE recall_pair MATCH '비밀'"); n != 0 {
		t.Fatal("secret indexed")
	}
}
func TestPairTokens(t *testing.T) {
	if got := recall.PairTokens("공부하는 공부 공 부 공🙂부 Café CAFÉ abc한글42"); got != "공부 부하 하는 공부 공 부 공 부 café café abc 한글 42" {
		t.Fatal(got)
	}
}
func TestKoreanLedgerParity(t *testing.T) {
	var fixture struct {
		Documents []struct{ ID, Origin, Body string }
		Queries   []struct {
			Name, Text string
			IDs        []string
		}
	}
	data, err := os.ReadFile("../../../testdata/korean/search.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	db := database(t)
	for _, doc := range fixture.Documents {
		if doc.Origin == "native" {
			admit(t, db, doc.ID, doc.Body)
		} else {
			write(t, db, eventSQL, doc.ID, doc.Body)
		}
	}
	drain(t, db)
	for _, q := range fixture.Queries {
		// The shipped path was one literal phrase; multi-term rules are covered
		// by the recall search tests.
		if len(strings.Fields(q.Text)) != 1 {
			continue
		}
		hits, err := ledger.New(db, nil).Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: q.Text})
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, hit := range hits {
			want = append(want, hit.ID)
		}
		table := "recall_text"
		if utf8.RuneCountInString(q.Text) == 2 {
			table = "recall_pair"
		}
		var got []string
		err = db.Read(t.Context(), func(tx *canon.Tx) error {
			rows, e := tx.QueryContext(t.Context(), "SELECT source_id FROM recall_doc WHERE kind='event' AND person='owner' AND holder='agent' AND replay=0 AND id IN (SELECT rowid FROM "+table+" WHERE "+table+" MATCH ?) ORDER BY julianday(occurred_at) DESC,source_id", canon.TrigramPhrase(q.Text))
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					return e
				}
				got = append(got, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %v != %v", q.Name, got, want)
		}
	}
}

func TestFailedMaterialsDoNotStarveLaterSources(t *testing.T) {
	db := database(t)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		for i := 0; i < 128; i++ {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,material_id,material_revision)
 VALUES(?,'conversation','owner','agent','person','{}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','observed','{"kind":"native"}','asset',1)`, fmt.Sprintf("missing-%03d", i)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	write(t, db, eventSQL, "available", "도서관")
	s := recall.New(db, nil)
	if n, err := s.DrainBatch(t.Context()); n != 0 || !errors.Is(err, recall.ErrBytesUnavailable) {
		t.Fatalf("failed batch=%d %v", n, err)
	}
	if n, err := s.DrainBatch(t.Context()); n != 1 || err != nil {
		t.Fatalf("later source starved: %d %v", n, err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_pending"); n != 128 {
		t.Fatalf("lost failed work: %d", n)
	}
}

func TestRollbackAndRetractionFence(t *testing.T) {
	db := database(t)
	rollback := errors.New("rollback")
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), eventSQL, "rolled-back", "도서관"); err != nil {
			return err
		}
		if err := recall.Index(t.Context(), tx, "event", "rolled-back", "도서관"); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 0 {
		t.Fatal("index escaped rollback")
	}
	write(t, db, eventSQL, "imported", "도서관")
	drain(t, db)
	write(t, db, "INSERT INTO retract_excluded_event VALUES('memory','imported')")
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 0 {
		t.Fatal("queued import survived retraction")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return recall.Index(t.Context(), tx, "event", "imported", "도서관") }); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 0 {
		t.Fatal("late write revived hidden event")
	}
	write(t, db, "DELETE FROM retract_excluded_event WHERE event_id='imported'")
	drain(t, db)
	if n := count(t, db, "SELECT count(*) FROM recall_doc"); n != 1 {
		t.Fatal("restored event not queued")
	}
}
