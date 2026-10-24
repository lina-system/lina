// SPDX-License-Identifier: Apache-2.0
package recall_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func admitAt(t *testing.T, db *canon.Store, id, conversation, person, agent, body string, at time.Time, replayOf string) {
	t.Helper()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: conversation, PersonID: person, AgentID: agent, ReplayOf: replayOf, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
}

func remember(t *testing.T, db *canon.Store, agent, holder, content string, scope memory.Scope, event string) string {
	t.Helper()
	resolve := func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
		return memory.Origin{Person: "owner", Agent: agent, Speaker: guard.Person, Quote: content, Scope: memory.Local}, nil
	}
	var id string
	write := memory.Write{Agent: agent, Person: "owner", ExplicitHolder: holder != "", Item: memory.Item{Kind: memory.Fact, Holder: memory.Holder(holder), Scope: scope, Subject: memory.Subject{Kind: memory.Person, ID: "owner"}, Content: content, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event, Precision: memory.Exact}}}}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		item, err := memory.Insert(t.Context(), tx, write, resolve)
		id = item.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func searchIDs(t *testing.T, db *canon.Store, scope recall.Scope, text string, limit int) []string {
	t.Helper()
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: text, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ID)
	}
	return ids
}

func TestSearchAnyTermRanksMoreTermsFirst(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "one", "conversation", "owner", "agent", "도서관", base.Add(2*time.Second), "")
	admitAt(t, db, "two", "conversation", "owner", "agent", "여행", base.Add(3*time.Second), "")
	admitAt(t, db, "both", "conversation", "owner", "agent", "도서관 여행", base.Add(time.Second), "")
	admitAt(t, db, "none", "conversation", "owner", "agent", "정원", base.Add(4*time.Second), "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	ids := searchIDs(t, db, scope, "도서관 여행", 0)
	if len(ids) != 3 || ids[0] != "both" {
		t.Fatalf("any-term order: %v", ids)
	}
	// Equal scores fall back to the newer event.
	if ids[1] != "two" || ids[2] != "one" {
		t.Fatalf("tie order: %v", ids)
	}
}

func TestSearchShortTermsReachMemoryAndRecordsOnly(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "event", "conversation", "owner", "agent", "xy 도서관", base, "")
	remember(t, db, "agent", "", "xy 기억", memory.Local, "event")
	write(t, db, `INSERT INTO records_index VALUES('record','inbox/record.md','blob','revision');
 INSERT INTO records_text VALUES('record','owner','memo','active','xy 기록','본문')`)
	drain(t, db)
	all := recall.Scope{Person: "owner", Agent: "agent"}
	if ids := searchIDs(t, db, all, "xy", 0); len(ids) != 2 {
		t.Fatalf("short non-Hangul reached events: %v", ids)
	}
	for _, hit := range mustMatch(t, db, all, "xy") {
		if hit.Kind == recall.Event {
			t.Fatalf("event matched a short non-Hangul term: %+v", hit)
		}
	}
	// A single Hangul syllable is short too.
	admitAt(t, db, "single", "conversation", "owner", "agent", "가", base, "")
	if ids := searchIDs(t, db, all, "가", 0); len(ids) != 0 {
		t.Fatalf("single syllable reached events: %v", ids)
	}
	// Two Hangul syllables match as a pair on every kind.
	if ids := searchIDs(t, db, all, "도서", 0); !reflect.DeepEqual(ids, []string{"event"}) {
		t.Fatalf("pair term: %v", ids)
	}
}

func mustMatch(t *testing.T, db *canon.Store, scope recall.Scope, text string) []recall.Hit {
	t.Helper()
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: text, Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestSearchSubstringReachesNonHangulWords(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "category", "conversation", "owner", "agent", "category theory", base, "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	if ids := searchIDs(t, db, scope, "cat", 0); !reflect.DeepEqual(ids, []string{"category"}) {
		t.Fatalf("substring: %v", ids)
	}
	if ids := searchIDs(t, db, scope, "categoryy", 0); len(ids) != 0 {
		t.Fatalf("over-long term matched: %v", ids)
	}
}

func TestSearchOneHitPerIdenticalTextAndReplayOptIn(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "first", "conversation", "owner", "agent", "같은 문장", base, "")
	admitAt(t, db, "second", "conversation", "owner", "agent", "같은 문장", base.Add(time.Minute), "")
	admitAt(t, db, "other-conversation", "other", "owner", "agent", "같은 문장", base.Add(2*time.Minute), "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	ids := searchIDs(t, db, scope, "같은", 0)
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"first", "other-conversation"}) {
		t.Fatalf("identical text counted twice: %v", ids)
	}
	// A replay copy is its own lineage only when asked for.
	admitAt(t, db, "copy", "conversation", "owner", "agent", "같은 문장", base.Add(3*time.Minute), "first")
	if ids := searchIDs(t, db, scope, "같은", 0); len(ids) != 2 {
		t.Fatalf("replay copy appeared by default: %v", ids)
	}
	// Asking for copies keeps them searchable, but the lineage still counts
	// once: the original represents the copy.
	scope.IncludeReplay = true
	ids = searchIDs(t, db, scope, "같은", 0)
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"first", "other-conversation"}) {
		t.Fatalf("replay opt-in: %v", ids)
	}
	// A copy whose original is outside the scope is its own result.
	scope.Conversation = "elsewhere"
	admitAt(t, db, "orphan-copy", "elsewhere", "owner", "agent", "같은 문장", base.Add(4*time.Minute), "first")
	if ids := searchIDs(t, db, scope, "같은", 0); !reflect.DeepEqual(ids, []string{"orphan-copy"}) {
		t.Fatalf("orphan copy: %v", ids)
	}
}

func TestSearchLineageKeepsEventAsItemCitation(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "event", "conversation", "owner", "agent", "도서관에서 공부했다.", base, "")
	item := remember(t, db, "agent", "", "도서관 공부", memory.Local, "event")
	scope := recall.Scope{Person: "owner", Agent: "agent"}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != item || hits[0].Kind != recall.Memory {
		t.Fatalf("lineage returned the event twice: %+v", hits)
	}
	results, err := recall.Search(t.Context(), db, nil, scope, recall.Query{Text: "도서관"})
	if err != nil || len(results) != 1 {
		t.Fatalf("search: %+v %v", results, err)
	}
	var cited []string
	for _, c := range results[0].Citations {
		if c.TargetKind == "event" {
			cited = append(cited, c.TargetID)
		}
	}
	if !reflect.DeepEqual(cited, []string{"event"}) {
		t.Fatalf("item citation missing: %+v", results[0].Citations)
	}
}

func TestSearchScopeExcludesForeignNeverLeavesAndRetracted(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "shared", "conversation", "owner", "agent", "도서관 공부", base, "")
	admitAt(t, db, "public", "conversation", "owner", "agent", "도서관 산책", base.Add(time.Second), "")
	remember(t, db, "other", "other", "도서관 개인", memory.Local, "shared")
	remember(t, db, "agent", "", "도서관 비밀", memory.NeverLeaves, "shared")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Memory}}
	if ids := searchIDs(t, db, scope, "도서관", 0); len(ids) != 1 {
		t.Fatalf("private holder leaked: %v", ids)
	}
	scope.ExternalModel = true
	if ids := searchIDs(t, db, scope, "도서관", 0); len(ids) != 0 {
		t.Fatalf("never-leaves reached external scope: %v", ids)
	}
	// The event that the never-leaves item rests on stays out of external scope,
	// and a common item citing it cannot bring it back.
	common := remember(t, db, "agent", "", "도서관 공부 공용", memory.Local, "shared")
	events := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}, ExternalModel: true}
	if ids := searchIDs(t, db, events, "도서관", 0); !reflect.DeepEqual(ids, []string{"public"}) {
		t.Fatalf("protected event leaked: %v", ids)
	}
	all := recall.Scope{Person: "owner", Agent: "agent", ExternalModel: true}
	for _, id := range searchIDs(t, db, all, "도서관", 0) {
		if id == common {
			t.Fatal("common item citing a protected event leaked")
		}
	}
	// A retraction through the one path removes the document in the same
	// transaction.
	write(t, db, `INSERT OR IGNORE INTO removal_applied VALUES(1,'erase','fixture'); INSERT INTO retract_target VALUES('event','public','owner','agent','agent','erased',50,'{}','erase')`)
	if ids := searchIDs(t, db, recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}, "산책", 0); len(ids) != 0 {
		t.Fatalf("retracted event returned: %v", ids)
	}
}

func TestSearchSummaryHitCarriesItsEvents(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "event", "conversation", "owner", "agent", "도서관에서 공부했다.", base, "")
	write(t, db, `INSERT INTO sum_node(id,conversation_id,level,model,prompt_version,built_at,start_at,end_at,epoch,status,text,dropped,revision)
 VALUES('summary','conversation',0,'fixture','1','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z',0,'ready','도서관 요약','',1);
 INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role)
 VALUES('summary','summary','1',0,'event','event','0',NULL,NULL,'event','covers')`)
	drain(t, db)
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Summary}}
	results, err := recall.Search(t.Context(), db, nil, scope, recall.Query{Text: "요약"})
	if err != nil || len(results) != 1 || results[0].Kind != recall.Summary {
		t.Fatalf("summary hit: %+v %v", results, err)
	}
	if len(results[0].Citations) != 1 || results[0].Citations[0].TargetID != "event" || results[0].Citations[0].Role != "covers" {
		t.Fatalf("summary did not point at its events: %+v", results[0].Citations)
	}
}

func TestSearchExcerptIsBoundedAndMasked(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	// Admission masks the credential shape before it reaches the index.
	body := strings.Repeat("앞말 ", 200) + "도서관" + strings.Repeat(" 뒷말", 200) + " api_key=synthetic-secret-value"
	admitAt(t, db, "long", "conversation", "owner", "agent", body, base.Add(time.Second), "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	results, err := recall.Search(t.Context(), db, nil, scope, recall.Query{Text: "도서관"})
	if err != nil || len(results) == 0 {
		t.Fatalf("excerpt search: %+v %v", results, err)
	}
	for _, result := range results {
		if result.ID != "long" {
			continue
		}
		if !strings.Contains(result.Excerpt, "도서관") || strings.Contains(result.Excerpt, "synthetic-secret-value") {
			t.Fatalf("excerpt: %.80q", result.Excerpt)
		}
		if runes := len([]rune(result.Excerpt)); runes > recall.ExcerptRunes+2 {
			t.Fatalf("excerpt not bounded: %d runes", runes)
		}
	}
}

func readFixture() ([]byte, error) {
	return os.ReadFile("../../../testdata/korean/search.json")
}

func TestSearchKoreanFixtureEventParity(t *testing.T) {
	var fixture struct {
		Documents []struct{ ID, Origin, Body string }
		Queries   []struct {
			Name, Text string
			IDs        []string
		}
	}
	data, err := readFixture()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	db := database(t)
	for _, doc := range fixture.Documents {
		if doc.Origin == "native" {
			admit(t, db, doc.ID, doc.Body)
			continue
		}
		write(t, db, eventSQL, doc.ID, doc.Body)
	}
	drain(t, db)
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	for _, q := range fixture.Queries {
		if len(strings.Fields(q.Text)) != 1 {
			continue
		}
		got := searchIDs(t, db, scope, q.Text, 0)
		want := append([]string(nil), q.IDs...)
		sort.Strings(got)
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: %v, want %v", q.Name, got, want)
		}
	}
}

func TestSearchVectorCandidatesKeepScopeBeforeFusion(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "public", "conversation", "owner", "agent", "산책", base, "")
	secret := remember(t, db, "agent", "", "도서관 비밀", memory.NeverLeaves, "public")
	vectors := func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{secret}, nil
	}
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Memory}, ExternalModel: true}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "산책", Vectors: vectors})
	if err != nil || len(hits) != 0 {
		t.Fatalf("never-leaves vector candidate: %+v %v", hits, err)
	}
	// The same candidate is a normal result once the scope admits it.
	scope.ExternalModel = false
	hits, err = recall.Match(t.Context(), db, scope, recall.Query{Text: "산책", Vectors: vectors})
	if err != nil || len(hits) != 1 || hits[0].ID != secret {
		t.Fatalf("scoped vector candidate: %+v %v", hits, err)
	}
}

func TestSearchReadsMaterialBackedEventBody(t *testing.T) {
	db := database(t)
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,material_id,material_revision)
 VALUES('material','conversation','owner','agent','person','{"kind":"tui"}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','recorded','{"kind":"import"}','m1',1)`)
	read := func(context.Context, string) (string, error) { return "도서관에서 공부했다.", nil }
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	results, err := recall.Search(t.Context(), db, read, scope, recall.Query{Text: "도서관", EventText: read})
	if err != nil || len(results) != 1 || results[0].ID != "material" {
		t.Fatalf("material-backed event: %+v %v", results, err)
	}
	if !strings.Contains(results[0].Excerpt, "도서관") {
		t.Fatalf("material excerpt: %q", results[0].Excerpt)
	}
}

func TestSearchVectorsFuseIntoTheRankedList(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "text", "conversation", "owner", "agent", "도서관", base, "")
	admitAt(t, db, "vector-only", "conversation", "owner", "agent", "산책", base.Add(time.Second), "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", Vectors: func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{"vector-only"}, nil
	}})
	if err != nil || len(hits) != 2 {
		t.Fatalf("fusion: %+v %v", hits, err)
	}
}

// requeue drops the published document so the source is pending again, as it
// is during import or recovery before the background drain.
func requeue(t *testing.T, db *canon.Store, kind, id string) {
	t.Helper()
	write(t, db, "DELETE FROM recall_doc WHERE kind=? AND source_id=?", kind, id)
	write(t, db, "INSERT OR IGNORE INTO recall_pending(kind, source_id) VALUES(?,?)", kind, id)
}

type queuedMaterials struct{ bodies map[ledger.Material]string }

func (m *queuedMaterials) Prepare(_ context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	ref := ledger.Material{ID: "material-" + strings.Repeat("0", len(m.bodies)), Revision: 1}
	if m.bodies == nil {
		m.bodies = map[ledger.Material]string{}
	}
	return queuedMaterialWrite{store: m, ref: ref, body: body.Text()}, nil
}

type queuedMaterialWrite struct {
	store *queuedMaterials
	ref   ledger.Material
	body  string
}

func (w queuedMaterialWrite) Insert(context.Context, *canon.Tx) (ledger.Material, error) {
	w.store.bodies[w.ref] = w.body
	return w.ref, nil
}
func (w queuedMaterialWrite) Close() error { return nil }
func (m *queuedMaterials) Read(_ context.Context, ref ledger.Material) (string, error) {
	body, ok := m.bodies[ref]
	if !ok {
		return "", ledger.ErrBytesUnavailable
	}
	return body, nil
}

func admitMaterial(t *testing.T, db *canon.Store, materials ledger.Materials, id, body string, at time.Time) {
	t.Helper()
	event, err := ledger.New(db, materials).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "conversation", PersonID: "owner", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if event.Material.ID == "" {
		t.Fatal("fixture stayed inline")
	}
}

func materialText(materials *queuedMaterials) recall.ReadEvent {
	return func(context.Context, string) (string, error) {
		for _, body := range materials.bodies {
			return body, nil
		}
		return "", ledger.ErrBytesUnavailable
	}
}

func TestQueuedMaterialRanksBeforeLimit(t *testing.T) {
	db := database(t)
	materials := &queuedMaterials{}
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "indexed", "conversation", "owner", "agent", "산책", base.Add(time.Hour), "")
	body := "도서관 산책 " + strings.Repeat("padding ", 4000)
	admitMaterial(t, db, materials, "queued", body, base)
	requeue(t, db, "event", "queued")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관 산책", Limit: 1, EventText: materialText(materials)})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "queued" {
		t.Fatalf("queued material lost the limit: %+v", hits)
	}
	// Same text, opposite queue order: newer occurred_at wins, not insert order.
	admitMaterial(t, db, materials, "older", "정원 산책 "+strings.Repeat("padding ", 4000), base)
	admitMaterial(t, db, materials, "newer", "정원 산책 "+strings.Repeat("padding ", 4000), base.Add(2*time.Hour))
	// Queue the older event first so a scan in insert order is not time order.
	requeue(t, db, "event", "older")
	requeue(t, db, "event", "newer")
	ids := matchIDs(t, db, scope, "정원", 0, func(ctx context.Context, id string) (string, error) {
		event, err := ledger.New(db, materials).Get(ctx, id)
		if err != nil {
			return "", err
		}
		return event.Body.DerivedText(), nil
	})
	if len(ids) < 2 || ids[0] != "newer" || ids[1] != "older" {
		t.Fatalf("queued material order: %v", ids)
	}
}

func matchIDs(t *testing.T, db *canon.Store, scope recall.Scope, text string, limit int, read recall.ReadEvent) []string {
	t.Helper()
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: text, Limit: limit, EventText: read})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ID)
	}
	return ids
}

func TestQueuedReplayCopyDoesNotHideIndexedEvents(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "original", "conversation", "owner", "agent", "같은 문장", base, "")
	admitAt(t, db, "other", "other", "owner", "agent", "같은 문장", base.Add(time.Minute), "")
	admitAt(t, db, "copy", "conversation", "owner", "agent", "같은 문장", base.Add(2*time.Minute), "original")
	requeue(t, db, "event", "copy")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}, IncludeReplay: true}
	ids := searchIDs(t, db, scope, "같은", 0)
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"original", "other"}) {
		t.Fatalf("queued replay copy cleared recall: %v", ids)
	}
}

func TestQueuedMaterialKeepsPendingScoreOrder(t *testing.T) {
	db := database(t)
	materials := &queuedMaterials{}
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "indexed", "conversation", "owner", "agent", "도서관 산책", base.Add(time.Hour), "")
	admitAt(t, db, "pending", "conversation", "owner", "agent", "도서관 산책", base, "")
	requeue(t, db, "event", "pending")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	before := searchIDs(t, db, scope, "도서관 산책", 0)
	admitMaterial(t, db, materials, "queued", "산책 "+strings.Repeat("padding ", 4000), base.Add(2*time.Hour))
	requeue(t, db, "event", "queued")
	after := matchIDs(t, db, scope, "도서관 산책", 0, func(ctx context.Context, id string) (string, error) {
		event, err := ledger.New(db, materials).Get(ctx, id)
		if err != nil {
			return "", err
		}
		return event.Body.DerivedText(), nil
	})
	kept := make([]string, 0, len(before))
	for _, id := range after {
		if id != "queued" {
			kept = append(kept, id)
		}
	}
	if !reflect.DeepEqual(kept, before) {
		t.Fatalf("queued material reordered published hits: before %v after %v", before, after)
	}
}

func TestQueuedMaterialDoesNotReorderIndexedHits(t *testing.T) {
	db := database(t)
	materials := &queuedMaterials{}
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "short", "conversation", "owner", "agent", "도서관", base.Add(time.Hour), "")
	admitAt(t, db, "long", "conversation", "owner", "agent", "도서관 산책 "+strings.Repeat("다른단어 ", 400), base, "")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	before := searchIDs(t, db, scope, "도서관 산책", 0)
	admitMaterial(t, db, materials, "queued", "산책 "+strings.Repeat("padding ", 4000), base.Add(2*time.Hour))
	requeue(t, db, "event", "queued")
	after := matchIDs(t, db, scope, "도서관 산책", 0, func(ctx context.Context, id string) (string, error) {
		event, err := ledger.New(db, materials).Get(ctx, id)
		if err != nil {
			return "", err
		}
		return event.Body.DerivedText(), nil
	})
	indexed := make([]string, 0, 2)
	for _, id := range after {
		if id != "queued" {
			indexed = append(indexed, id)
		}
	}
	if !reflect.DeepEqual(indexed, before) {
		t.Fatalf("queued material reordered indexed hits: before %v after %v", before, after)
	}
}

func TestQueuedMaterialEventsStayOutOfOtherKinds(t *testing.T) {
	db := database(t)
	materials := &queuedMaterials{}
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "source", "conversation", "owner", "agent", "도서관에서 공부했다.", base, "")
	item := remember(t, db, "agent", "", "도서관 기억", memory.Local, "source")
	body := "도서관 원문 " + strings.Repeat("padding ", 4000)
	admitMaterial(t, db, materials, "raw", body, base)
	requeue(t, db, "event", "raw")
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Memory}}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", EventText: materialText(materials)})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.Kind == recall.Event {
			t.Fatalf("memory search returned a queued event: %+v", hits)
		}
	}
	if len(hits) != 1 || hits[0].ID != item {
		t.Fatalf("memory hit: %+v", hits)
	}
}

func TestVectorCandidatesUseTheFullScope(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "public", "conversation", "owner", "agent", "도서관 산책", base, "")
	admitAt(t, db, "basis", "conversation", "owner", "agent", "근거 문장", base.Add(time.Second), "")
	admitAt(t, db, "open", "conversation", "owner", "agent", "별도 문장", base.Add(time.Minute), "")
	admitAt(t, db, "copy", "conversation", "owner", "agent", "도서관 복사", base.Add(2*time.Second), "public")
	secret := remember(t, db, "agent", "", "도서관 비밀", memory.NeverLeaves, "public")
	future := remember(t, db, "agent", "", "도서관 이후", memory.Local, "basis")
	drain(t, db)
	vectors := func(_ context.Context, _ recall.Scope, _ string) ([]string, error) {
		return []string{"copy", secret, future, "open"}, nil
	}
	external := recall.Scope{Person: "owner", Agent: "agent", ExternalModel: true, Kinds: []recall.Kind{recall.Event, recall.Memory}}
	hits, err := recall.Match(t.Context(), db, external, recall.Query{Text: "없는말", Vectors: vectors})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, hit := range hits {
		got[hit.ID] = true
	}
	if got[secret] || got["copy"] {
		t.Fatalf("vector scope leaked: %+v", hits)
	}
	if !got["open"] || !got[future] {
		t.Fatalf("visible vector hits dropped: %+v", hits)
	}
	late := external
	late.Since = base.Add(time.Hour)
	hits, err = recall.Match(t.Context(), db, late, recall.Query{Text: "없는말", Vectors: vectors})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.Kind == recall.Event {
			t.Fatalf("vector hit ignored the time filter: %+v", hits)
		}
	}
	write(t, db, "UPDATE memory_item SET valid_from=? WHERE id=?", time.Now().Add(24*time.Hour).Unix(), future)
	drain(t, db)
	stale := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Memory}}
	hits, err = recall.Match(t.Context(), db, stale, recall.Query{Text: "없는말", Vectors: func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{future}, nil
	}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("vector hit ignored validity: %+v %v", hits, err)
	}
}
