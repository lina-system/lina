// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
)

func TestMemoryEvidenceReadsCurrentSpan(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "material"}[large], func(t *testing.T) {
			db := memoryDB(t)
			materials := &sourceMaterials{bodies: map[ledger.Material]string{}}
			events := ledger.New(db, materials)
			body := "앞말 한국어 차를 좋아해요 api_key=synthetic-secret-value 뒷말"
			if large {
				body += strings.Repeat(" 큰 본문", ledger.InlineBytes)
			}
			event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
			if err != nil {
				t.Fatal(err)
			}
			flow := NewMemory(db, materials, nil)
			receipt, err := flow.Remember(t.Context(), rememberInput("likes tea"))
			if err != nil {
				t.Fatal(err)
			}

			full, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "drink?", Query: "tea", At: 100})
			if err != nil || len(full.Sources) != 1 || full.Sources[0].Evidence[0].Quote != event.Body.DerivedText() {
				t.Fatal("whole derived span mismatch", err)
			}
			start := int64(len("앞말 "))
			end := int64(len("앞말 한국어 차를 좋아해요"))
			err = db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "UPDATE cite SET span_start=?,span_end=? WHERE view_kind='memory' AND view_id=?", start, end, receipt.ID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "drink?", Query: "tea", At: 100})
			if err != nil || len(answer.Sources) != 1 {
				t.Fatalf("recall: %+v %v", answer, err)
			}
			if got := answer.Sources[0].Evidence[0].Quote; got != "한국어 차를 좋아해요" {
				t.Fatalf("quote not read from span: %.80q", got)
			}
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				for _, table := range []string{"memory_item", "memory_revision"} {
					var raw []byte
					if err := tx.QueryRowContext(t.Context(), "SELECT record FROM "+table+" LIMIT 1").Scan(&raw); err != nil {
						return err
					}
					var item memory.Item
					if err := json.Unmarshal(raw, &item); err != nil {
						return err
					}
					if strings.Contains(string(raw), "한국어") || item.Evidence[0].Quote != "" {
						t.Errorf("%s copied event text", table)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if large {
				delete(materials.bodies, event.Material)
				answer, err = flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "drink?", Query: "tea", At: 100})
				if err != nil || len(answer.Sources) != 1 || answer.Sources[0].Evidence[0].Quote != "" || answer.Sources[0].Evidence[0].Status != "bytes_unavailable" {
					t.Fatalf("unavailable source kept quote: %+v %v", answer, err)
				}
			}
		})
	}
}

func TestMemoryEvidenceLegacyImportAndRemoval(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "한국어 원문 api_key=synthetic-secret-value 끝"})
	if err != nil {
		t.Fatal(err)
	}
	// Represent a sealed imported event. Admission already produced the masked
	// display; the immutable import origin is fixture metadata, not a live edit.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		var trigger string
		if err := tx.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_schema WHERE name='conv_event_no_update'").Scan(&trigger); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), "DROP TRIGGER conv_event_no_update"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), `UPDATE conv_event SET surface='{"kind":"import"}', origin='{"kind":"import","system":"hermes","instance":"fixture"}' WHERE id='event'`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), trigger); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO import_batch VALUES('batch','2026-01-01T00:00:00Z');
INSERT INTO import_record VALUES('conversation','conversation','A','session',8,'','[]',NULL);
INSERT INTO import_conversation VALUES('conversation','batch',printf('%064d',0),'{}',0);
INSERT INTO import_seal VALUES('conversation','event','fixture-backup')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var item memory.Item
	resolve := func(_ context.Context, _ *canon.Tx, _ memory.Evidence) (memory.Origin, error) {
		return memory.Origin{Person: "person", Agent: "A", Speaker: guard.Person, Quote: event.Body.DerivedText(), Scope: memory.Local}, nil
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		in := rememberInput("likes tea")
		in.Item.Basis = memory.Imported
		var err error
		item, err = memory.Promote(t.Context(), tx, in, "confirmed", resolve)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := memory.New(db).WithQuotes(events.Citation)
	read := func(agent string) memory.Evidence {
		t.Helper()
		hits, err := store.Recall(t.Context(), memory.RecallScope{Agent: agent, Person: "person"}, memory.Query{At: 100})
		if err != nil || len(hits) != 1 {
			t.Fatalf("recall: %v %v", hits, err)
		}
		return hits[0].Evidence[0]
	}
	if got := read("A"); got.Quote != event.Body.DerivedText() || strings.Contains(got.Quote, "synthetic-secret") {
		t.Fatalf("import projection: %+v", got)
	}
	if got := read("B"); got.Quote != "" || got.Tombstone != nil {
		t.Fatalf("cross-agent: %+v", got)
	}
	// Legacy records remain readable, but their copies cannot override canon.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE memory_item SET record=json_set(record,'$.evidence[0].quote','stale legacy copy') WHERE id=?`, item.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := read("A"); got.Quote != event.Body.DerivedText() {
		t.Fatalf("legacy source not resolved: %+v", got)
	}
	// A new revision of that old item must strip the legacy copy too, while
	// retaining the citation span originally filled by the migration/writer.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		in := rememberInput("likes tea")
		in.Item.Basis = memory.Imported
		in.Item.Evidence = append(in.Item.Evidence, memory.Evidence{Kind: memory.External, ID: "observation", Precision: memory.Session})
		_, err := memory.Promote(t.Context(), tx, in, "confirmed", func(ctx context.Context, tx *canon.Tx, e memory.Evidence) (memory.Origin, error) {
			o, err := resolve(ctx, tx, e)
			o.Verified = true
			return o, err
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var copies int
		err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_revision r,json_each(r.record,'$.evidence') e WHERE json_extract(e.value,'$.quote') IS NOT NULL`).Scan(&copies)
		if copies != 0 {
			t.Errorf("new imported/legacy revisions retain %d quotes", copies)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO removal_applied VALUES(1,'erase','fixture'); INSERT INTO retract_target VALUES('event','event','person','A','A','erased',50,'{}','erase')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := read("A"); got.Quote != "" || got.Status != "tombstone" || got.Tombstone == nil || got.Tombstone.At != 50 {
		t.Fatalf("erased event: %+v", got)
	}
	if got := read("B"); got.Quote != "" || got.Tombstone != nil {
		t.Fatalf("foreign tombstone disclosed: %+v", got)
	}
}

func TestMemoryPersonBriefResolvesEventQuote(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "한국어 차를 좋아해요 api_key=synthetic-secret-value"})
	if err != nil {
		t.Fatal(err)
	}
	store := memory.New(db).WithQuotes(events.Citation)
	resolve := func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
		return memory.Origin{Person: "person", Agent: "A", Speaker: guard.Person, Scope: memory.Local, Quote: event.Body.DerivedText()}, nil
	}
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	ref := memory.Evidence{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}
	task, ready, err := store.PrepareBrief(t.Context(), scope, "tea", 1, 2, 100, []memory.Evidence{ref}, resolve)
	if err != nil || !ready {
		t.Fatalf("prepare: %v %v", ready, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return memory.PublishBrief(t.Context(), tx, task, []memory.BriefSentence{{Text: "Tea preference.", Citations: []memory.Evidence{ref}}}, 100, resolve)
	}); err != nil {
		t.Fatal(err)
	}
	brief, err := store.PersonBrief(t.Context(), scope, "tea", 1, 100, resolve)
	if err != nil || len(brief) != 1 || len(brief[0].Citations) != 1 || brief[0].Citations[0].Quote != event.Body.DerivedText() {
		t.Fatalf("brief: %+v %v", brief, err)
	}
}
