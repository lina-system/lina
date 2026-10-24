// SPDX-License-Identifier: Apache-2.0
package recall_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/retract"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestVectorMigration(t *testing.T) {
	db := database(t)
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var model string
		var dimension, rows int
		if err := tx.QueryRowContext(t.Context(), "SELECT model,dimension FROM search_index_config WHERE name='recall'").Scan(&model, &dimension); err != nil {
			return err
		}
		if model != "BAAI/bge-m3" || dimension != 1024 {
			t.Fatalf("default model=%s dimension=%d", model, dimension)
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_vec").Scan(&rows)
	})
	if err != nil {
		t.Fatal(err)
	}
}

type fakeEmbeddings struct {
	model            string
	dimension        int
	calls, documents int
	texts            []string
	fail             bool
	during           func()
}

func (f *fakeEmbeddings) VectorConfig() recall.VectorConfig {
	return recall.VectorConfig{Endpoint: "http://localhost/embeddings", Model: f.model, Dimension: f.dimension}
}
func (f *fakeEmbeddings) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	f.documents += len(texts)
	f.texts = append(f.texts, texts...)
	if f.during != nil {
		fn := f.during
		f.during = nil
		fn()
	}
	if f.fail {
		return nil, embedding.ErrUnavailable
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = make([]float32, f.dimension)
		out[i][0] = 1
		if strings.Contains(text, "garden") || strings.Contains(text, "plants") {
			out[i][0] = 0
			out[i][1] = 1
		}
	}
	return out, nil
}
func vectorCount(t *testing.T, db *canon.Store) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_vec").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
func finishVectors(t *testing.T, db *canon.Store, v *recall.Vectors) {
	t.Helper()
	for range 50 {
		if _, err := v.DrainBatch(t.Context()); err != nil {
			t.Fatal(err)
		}
		var ready int
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT ready FROM search_index_config WHERE name='recall'").Scan(&ready)
		}); err != nil {
			t.Fatal(err)
		}
		if ready == 1 {
			return
		}
	}
	t.Fatal("embedding queue did not finish")
}
func TestVectorsEligibilityRebuildForgetAndFallback(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "garden", "conversation", "owner", "agent", "garden plants", base, "")
	admitAt(t, db, "lexical", "conversation", "owner", "agent", "library books", base.Add(time.Second), "")
	admitAt(t, db, "secret", "conversation", "owner", "agent", "classified", base.Add(2*time.Second), "")
	write(t, db, `INSERT INTO ev_span(event_id,start,end,sensitivity) VALUES('secret',0,10,'secret')`)
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,origin,occurred_at,recorded_at,time_quality,body) SELECT 'imported','imported-conversation',person_id,agent_id,speaker_kind,surface,'{"kind":"import","system":"fixture"}',occurred_at,recorded_at,time_quality,'imported garden' FROM conv_event WHERE id='garden'`)
	drain(t, db)
	f := &fakeEmbeddings{model: "first", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	lexical := searchIDs(t, db, scope, "library", 12)
	match := func(text string) []recall.Hit {
		t.Helper()
		hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: text, Limit: 12, Vectors: v.Query})
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	if hits := match("library"); len(hits) != len(lexical) || hits[0].ID != lexical[0] {
		t.Fatal("rebuild changed lexical recall")
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 2 {
		t.Fatalf("eligible vectors=%d", vectorCount(t, db))
	}
	for _, text := range f.texts {
		if strings.Contains(text, "classified") || strings.Contains(text, "imported") {
			t.Fatal("ineligible text sent")
		}
	}
	if hits := match("plants growing"); len(hits) < 1 || hits[0].ID != "garden" {
		t.Fatalf("semantic recall: %v", hits)
	}
	if err := recall.SetImportConsent(t.Context(), db, "imported-conversation", true); err != nil {
		t.Fatal(err)
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 3 {
		t.Fatal("consented import missing")
	}
	if err := recall.SetImportConsent(t.Context(), db, "imported-conversation", false); err != nil {
		t.Fatal(err)
	}
	if vectorCount(t, db) != 0 {
		t.Fatal("revocation retained vectors")
	}
	finishVectors(t, db, v)
	f2 := &fakeEmbeddings{model: "second", dimension: 3}
	v, err = recall.NewVectors(t.Context(), db, nil, f2)
	if err != nil {
		t.Fatal(err)
	}
	if vectorCount(t, db) != 0 {
		t.Fatal("old model retained")
	}
	if hits := match("library"); len(hits) != len(lexical) || hits[0].ID != lexical[0] {
		t.Fatal("model rebuild changed lexical recall")
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 2 {
		t.Fatal("rebuild incomplete")
	}
	item := remember(t, db, "agent", "", "garden preference", memory.Local, "garden")
	finishVectors(t, db, v)
	f2.fail = true
	if hits := match("library"); len(hits) != len(lexical) || hits[0].ID != lexical[0] {
		t.Fatal("endpoint failure changed lexical recall")
	}
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer removals.Close()
	if err := removals.Forget(t.Context(), retract.Request{Person: "owner", Targets: []retract.Ref{{Kind: retract.Memory, ID: item}}}); err != nil {
		t.Fatal(err)
	}
	if count(t, db, "SELECT count(*) FROM recall_vec v JOIN recall_doc d ON d.id=v.rowid WHERE d.source_id=?", item) != 0 {
		t.Fatal("forget retained a vector")
	}
}

func TestVectorsBoundedBatchAndLateRemoval(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	for i := range 40 {
		admitAt(t, db, fmt.Sprint(i), "conversation", "owner", "agent", fmt.Sprintf("garden %d", i), base.Add(time.Duration(i)*time.Second), "")
	}
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	f.during = func() { write(t, db, "DELETE FROM recall_doc WHERE source_id='0'") }
	n, err := v.DrainBatch(t.Context())
	if err != nil || n != 31 || f.documents != 32 {
		t.Fatalf("batch=%d docs=%d err=%v", n, f.documents, err)
	}
	if vectorCount(t, db) != 31 {
		t.Fatal("late result resurrected removed document")
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 39 {
		t.Fatal("queue did not drain")
	}
}
func TestVectorsScopeBeforeLimitAndSecretProjection(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "allowed", "conversation", "owner", "agent", "library books", base, "")
	for i := range 105 {
		admitAt(t, db, fmt.Sprint(i), "other-conversation", "owner", "other", fmt.Sprintf("plants %d", i), base.Add(time.Duration(i)*time.Second), "")
	}
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	finishVectors(t, db, v)
	ids, err := v.Query(t.Context(), recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}, "plants")
	if err != nil || !reflect.DeepEqual(ids, []string{"event\x00allowed"}) {
		t.Fatalf("scoped vectors: %v %v", ids, err)
	}
}

func TestMessengerImportConsentAndChatSwitch(t *testing.T) {
	db := database(t)
	admit(t, db, "imported-message", "garden plants")
	write(t, db, `INSERT INTO messenger_source(person,platform,account,mode,adapter,state,connected_at) VALUES('owner','fixture','account','official','fixture','import_only','2026-01-01T00:00:00Z');
 INSERT INTO messenger_chat(platform,account,chat,kind) VALUES('fixture','account','chat','direct');
 INSERT INTO messenger_record(platform,account,chat,message,event,occurred_at) VALUES('fixture','account','chat','message','imported-message','2026-01-01T00:00:00Z');
 INSERT INTO messenger_record_revision VALUES('fixture','account','chat','message',1,'imported-message');
 INSERT INTO import_batch VALUES('batch','2026-01-01T00:00:00Z');
 INSERT INTO messenger_legacy_ref VALUES('fixture','fixture','message','message','fixture','account','chat','message','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','batch','2026-01-01T00:00:00Z','{}');`)
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, db)
	finishVectors(t, db, v)
	if f.calls != 0 || vectorCount(t, db) != 0 {
		t.Fatal("unconsented messenger import sent")
	}
	if err = recall.SetImportConsent(t.Context(), db, "conversation", true); err != nil {
		t.Fatal(err)
	}
	drain(t, db)
	finishVectors(t, db, v)
	if vectorCount(t, db) != 0 {
		t.Fatal("import consent granted source consent")
	}
	write(t, db, "INSERT INTO messenger_embed_source VALUES('owner','fixture','account')")
	drain(t, db)
	finishVectors(t, db, v)
	if vectorCount(t, db) != 2 {
		t.Fatal("consented messenger import not embedded")
	}
	write(t, db, "UPDATE messenger_chat SET embed=0")
	if vectorCount(t, db) != 0 {
		t.Fatal("disabled chat retained vector")
	}
	drain(t, db)
	finishVectors(t, db, v)
	if f.calls != 1 {
		t.Fatal("disabled chat embedded")
	}
}
func TestVectorConsentDoesNotAuthorizeFutureImport(t *testing.T) {
	db := database(t)
	admit(t, db, "template", "garden")
	insert := func(id string) {
		t.Helper()
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,origin,occurred_at,recorded_at,time_quality,body) SELECT ?,'imported-conversation',person_id,agent_id,speaker_kind,surface,'{"kind":"import"}',occurred_at,recorded_at,time_quality,? FROM conv_event WHERE id='template'`, id, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		drain(t, db)
	}
	insert("first-import")
	if err := recall.SetImportConsent(t.Context(), db, "imported-conversation", true); err != nil {
		t.Fatal(err)
	}
	insert("future-import")
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 2 {
		t.Fatal("future import was preauthorized")
	}
	for _, text := range f.texts {
		if text == "future-import" {
			t.Fatal("future import sent")
		}
	}
}
func TestVectorPublicationFencedByConsentGeneration(t *testing.T) {
	db := database(t)
	admit(t, db, "native", "garden")
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	f.during = func() {
		write(t, db, "UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall'")
	}
	n, err := v.DrainBatch(t.Context())
	if err != nil || n != 0 || vectorCount(t, db) != 0 {
		t.Fatalf("late generation n=%d err=%v", n, err)
	}
	finishVectors(t, db, v)
	if vectorCount(t, db) != 1 {
		t.Fatal("generation did not retry")
	}
}

func TestVectorDeletionRollsBackWithSource(t *testing.T) {
	db := database(t)
	admit(t, db, "event", "garden")
	f := &fakeEmbeddings{model: "test", dimension: 2}
	v, err := recall.NewVectors(t.Context(), db, nil, f)
	if err != nil {
		t.Fatal(err)
	}
	finishVectors(t, db, v)
	failure := errors.New("abort source transaction")
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DELETE FROM recall_doc"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) || vectorCount(t, db) != 1 || count(t, db, "SELECT count(*) FROM recall_doc") != 1 {
		t.Fatal("failed removal committed partially")
	}
}

func TestFusionTiesUseKindWhenSourceIDsCollide(t *testing.T) {
	db := database(t)
	admit(t, db, "shared", "garden plants")
	write(t, db, `INSERT INTO records_index VALUES('shared','inbox/shared.md','blob','revision');
 INSERT INTO records_text VALUES('shared','owner','memo','active','garden plants','');
 INSERT INTO records_activity VALUES('shared','2023-11-14T22:13:20Z');`)
	drain(t, db)
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event, recall.Record}}
	lexical, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "garden", Limit: 12})
	if err != nil || len(lexical) != 2 {
		t.Fatalf("lexical=%v err=%v", lexical, err)
	}
	query := func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{string(lexical[1].Kind) + "\x00shared", string(lexical[0].Kind) + "\x00shared"}, nil
	}
	for range 64 {
		hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "garden", Limit: 12, Vectors: query})
		if err != nil || len(hits) != 2 || hits[0].Kind != recall.Event || hits[1].Kind != recall.Record {
			t.Fatalf("non-deterministic fusion tie: %v %v", hits, err)
		}
	}
}
