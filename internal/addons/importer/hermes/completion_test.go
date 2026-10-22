// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
)

func TestRehearsalSealPromotion(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	report, err := p.Rehearse()
	if err != nil || report.References != 11 || len(report.Tables) == 0 || len(report.Speakers) != 1 {
		t.Fatalf("rehearsal: %+v %v", report, err)
	}
	for _, table := range []string{"import_record", "conv_event", "memory_item"} {
		assertCount(t, db, table, 0)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	batches, err := p.PromotionBatches(PromotionPolicy{})
	if err != nil || len(batches) != 1 {
		t.Fatalf("batches: %+v %v", batches, err)
	}
	accepted := []string{batches[0].ID}
	if _, err = p.Promote(t.Context(), db, PromotionPolicy{}, accepted, "confirmation"); err == nil {
		t.Fatal("promoted before seal/backup")
	}
	recall := func(want int) {
		t.Helper()
		hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "agent", Person: "person"}, memory.Query{Text: "Synthetic request", At: 1000})
		if err != nil || len(hits) != want {
			t.Fatalf("recall: %+v %v", hits, err)
		}
		if want > 0 && (hits[0].Basis != memory.Imported || hits[0].Evidence[0].Quote != "Synthetic request") {
			t.Fatalf("lost evidence: %+v", hits)
		}
	}
	recall(0)
	calls := 0
	backup := func(context.Context) (string, error) { calls++; return "verified-generation", nil }
	sealed, err := p.Seal(t.Context(), db, backup)
	if err != nil || sealed.AddedSeals != 1 || calls != 1 {
		t.Fatalf("seal: %+v %v calls=%d", sealed, err, calls)
	}
	recall(0)
	assertCount(t, db, "conv_event", 4)
	// Imported person events must not enter ordinary automatic extraction.
	batch, err := memory.New(db).PrepareExtraction(t.Context(), memory.RecallScope{Agent: "agent", Person: "person"}, 0, 100, time.Minute, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
		t.Error("import reached native extraction")
		return memory.Origin{}, nil
	})
	if err != nil || len(batch.Utterances) != 0 {
		t.Fatalf("extraction: %+v %v", batch, err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "1"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = event.Message(); err == nil {
		t.Fatal("import became request history")
	}
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "native", ConversationID: event.ConversationID, AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Now(), RecordedAt: time.Now(), TimeQuality: "observed", Body: "new"})
	if err == nil {
		t.Fatal("native event joined sealed range")
	}
	n, err := p.Promote(t.Context(), db, PromotionPolicy{}, accepted, "confirmation")
	if err != nil || n != 1 {
		t.Fatalf("promotion: %d %v", n, err)
	}
	recall(1)
	n, err = p.Promote(t.Context(), db, PromotionPolicy{}, accepted, "another-confirmation")
	if err != nil || n != 0 {
		t.Fatalf("repeat promotion: %d %v", n, err)
	}
	again, err := p.Seal(t.Context(), db, backup)
	if err != nil || again.AddedSeals != 0 || calls != 1 {
		t.Fatalf("repeat seal: %+v %v calls=%d", again, err, calls)
	}
	assertCount(t, db, "memory_item", 1)
}

func TestSealVerificationAndBackupFailure(t *testing.T) {
	p, err := Prepare(t.Context(), importRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	fail := errors.New("backup unavailable")
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "", fail }); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	batches, _ := p.PromotionBatches(PromotionPolicy{})
	if _, err = p.Promote(t.Context(), db, PromotionPolicy{}, []string{batches[0].ID}, "confirm"); err == nil {
		t.Fatal("backup failure allowed promotion")
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "conv_event", 4)
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE import_event SET body='corrupt' WHERE speaker_kind='person'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { t.Fatal("backed up unverified rows"); return "", nil }); !errors.Is(err, ErrConflict) {
		t.Fatalf("corruption: %v", err)
	}
}

func TestSealRejectsChangedAttributionAndMissingLinks(t *testing.T) {
	for _, query := range []string{
		"UPDATE import_event SET speaker_kind='person',person_id='person' WHERE speaker_kind='tool'",
		"DELETE FROM import_link WHERE kind='child'",
		"DELETE FROM import_event_relation",
		"INSERT INTO import_event_relation SELECT id,id,'pairs_with',0 FROM import_event LIMIT 1",
	} {
		t.Run(query, func(t *testing.T) {
			p, err := Prepare(t.Context(), importRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			db := importStore(t)
			if _, err = p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if err = db.Write(t.Context(), func(tx *canon.Tx) error { _, err := tx.ExecContext(t.Context(), query); return err }); err != nil {
				t.Fatal(err)
			}
			_, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil })
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("seal accepted damaged staging: %v", err)
			}
			assertCount(t, db, "conv_event", 0)
		})
	}
}

func TestFinalBackupResumesAfterPromotion(t *testing.T) {
	p, err := Prepare(t.Context(), importRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	batches, _ := p.PromotionBatches(PromotionPolicy{})
	fail := errors.New("final backup failed")
	calls := 0
	capture := func(context.Context) (string, error) {
		calls++
		if calls == 2 {
			return "", fail
		}
		return "generation", nil
	}
	_, n, err := p.Finish(t.Context(), db, PromotionPolicy{}, []string{batches[0].ID}, "confirmed", capture)
	if !errors.Is(err, fail) || n != 1 {
		t.Fatalf("final backup failure: %d %v", n, err)
	}
	_, n, err = p.Finish(t.Context(), db, PromotionPolicy{}, []string{batches[0].ID}, "confirmed", capture)
	if err != nil || n != 0 || calls != 3 {
		t.Fatalf("resume final backup: n=%d calls=%d err=%v", n, calls, err)
	}
	assertCount(t, db, "memory_item", 1)
}

func TestPromotionHistoryFiltersAndSessionEvidence(t *testing.T) {
	req := importRequest(t)
	w := honchoWorkspace{profile: "profile", workspace: "workspace"}
	for _, fields := range []map[string]any{
		{"kind": "conclusion", "id": "old", "content": "outdated", "timestamp": int64(50)},
		{"kind": "conclusion", "id": "new", "content": "current preference", "timestamp": int64(200), "supersedes": "[\"old\"]"},
		{"kind": "card", "id": "duplicate", "content": "current preference", "timestamp": int64(190)},
		{"kind": "card", "id": "persona", "content": "You must answer briefly", "timestamp": int64(220)},
		{"kind": "card", "id": "world", "content": "unrelated weather", "subject_kind": "world", "timestamp": int64(210)},
		{"kind": "card", "id": "deleted", "content": "deleted preference", "deleted": "true", "timestamp": int64(215)},
		{"kind": "session_summary", "id": "summary", "content": "summary invented fact", "timestamp": int64(230)},
		{"kind": "card", "id": "secret", "content": "password=synthetic-secret", "timestamp": int64(250)},
		{"kind": "card", "id": "dated", "content": "stale appointment", "timestamp": int64(10)},
	} {
		fields["session_id"] = "synthetic-session"
		w.history = append(w.history, typedRow(fields))
	}
	p, err := prepare(t.Context(), req, []honchoWorkspace{w})
	if err != nil {
		t.Fatal(err)
	}
	policy := PromotionPolicy{Horizon: 150}
	batches, err := p.PromotionBatches(policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || !batches[0].PersonaProposal || batches[1].Quote != "current preference" || len(batches[1].Sources) != 2 || batches[1].Precision != memory.Session {
		t.Fatalf("bad grouping/filtering: %+v", batches)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Promote(t.Context(), db, policy, []string{batches[0].ID}, "confirm"); err == nil {
		t.Fatal("persona instruction promoted")
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "agent", Person: "person"}, memory.Query{Text: "preference", At: 1000})
	if err != nil || len(hits) != 0 {
		t.Fatal("unpromoted history reached recall", hits, err)
	}
	if _, err = p.Promote(t.Context(), db, policy, []string{batches[1].ID}, "confirm"); err != nil {
		t.Fatal(err)
	}
	hits, err = memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "agent", Person: "person"}, memory.Query{Text: "preference", At: 1000})
	if err != nil || len(hits) != 1 {
		t.Fatal(hits, err)
	}
	if len(hits[0].Evidence) != 2 || hits[0].Evidence[0].Kind != memory.External || hits[0].Evidence[0].Precision != memory.Session {
		t.Fatalf("precision raised: %+v", hits)
	}
	// Receipts prevent a second approval from resurrecting a forgotten item.
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE memory_item SET state='retracted' WHERE id=?", hits[0].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Promote(t.Context(), db, policy, []string{batches[1].ID}, "again"); err != nil || n != 0 {
		t.Fatalf("resurrection: %d %v", n, err)
	}
}

func TestSealedSecretsStayOutOfSearchAndPromotion(t *testing.T) {
	req := importRequest(t)
	for _, profile := range req.Profiles {
		sqlSource(t, profile.LCMPath, "UPDATE messages SET content='password=synthetic-secret-value' WHERE store_id=1")
		sqlSource(t, profile.StatePath, "UPDATE messages SET content='password=synthetic-secret-value' WHERE id=1")
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	report, err := p.Rehearse()
	if err != nil || report.SecretSpans == 0 {
		t.Fatal("scan missing", err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
		t.Fatal(err)
	}
	events := ledger.New(db, nil)
	hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "person", Text: "synthetic-secret-value", Origin: "import"})
	if err != nil || len(hits) != 0 {
		t.Fatalf("secret indexed: %+v %v", hits, err)
	}
	batches, err := p.PromotionBatches(PromotionPolicy{})
	if err != nil || len(batches) != 0 {
		t.Fatalf("secret proposed: %+v %v", batches, err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal("masked projection changed original hash", err)
	}
}
