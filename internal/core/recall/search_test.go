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
	"github.com/thisisjun786/lina/internal/core/compaction"
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

func TestSearchVectorCandidatesStayInsideScope(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0).UTC()
	admitAt(t, db, "open", "conversation", "owner", "agent", "도서관 공개", base.Add(48*time.Hour), "")
	admitAt(t, db, "early", "conversation", "owner", "agent", "도서관 이른", base, "")
	admitAt(t, db, "protected", "conversation", "owner", "agent", "도서관 보호", base.Add(48*time.Hour), "")
	admitAt(t, db, "elsewhere", "other", "owner", "agent", "도서관 다른대화", base.Add(48*time.Hour), "")
	secret := remember(t, db, "agent", "", "비밀 기억", memory.NeverLeaves, "protected")
	private := remember(t, db, "agent", "", "개인 기억", memory.Private, "open")
	write(t, db, `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES('stale','owner','all','person','owner','지난 기억','지난 기억','local','active',?,?,'',0,'{"revision":1}')`, base.Add(-48*time.Hour).Unix(), base.Unix())
	indexMemory(t, db, "stale", "지난 기억")

	vectors := func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{secret, private, "stale", "protected", "early", "elsewhere", "open"}, nil
	}
	ids := func(scope recall.Scope) []string {
		t.Helper()
		hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", Vectors: vectors})
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(hits))
		for _, hit := range hits {
			out = append(out, hit.ID)
		}
		return out
	}
	external := recall.Scope{Person: "owner", Agent: "agent", ExternalModel: true}
	got := ids(external)
	for _, id := range []string{secret, "protected"} {
		if slices.Contains(got, id) {
			t.Fatalf("external model returned %s: %v", id, got)
		}
	}
	// private stays readable on an external-model scope. elsewhere is not
	// evidence of that item, so lineage does not hide it.
	if !slices.Contains(got, private) || !slices.Contains(got, "elsewhere") {
		t.Fatalf("external model dropped an allowed hit: %v", got)
	}

	projected := external
	projected.ExternalModel = false
	projected.Projection = true
	got = ids(projected)
	if slices.Contains(got, private) || slices.Contains(got, secret) {
		t.Fatalf("projection returned a closed memory: %v", got)
	}

	window := recall.Scope{Person: "owner", Agent: "agent", Since: base.Add(24 * time.Hour), Kinds: []recall.Kind{recall.Event}}
	got = ids(window)
	if slices.Contains(got, "early") || !slices.Contains(got, "open") {
		t.Fatalf("time filter: %v", got)
	}

	current := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Memory}}
	got = ids(current)
	if slices.Contains(got, "stale") {
		t.Fatalf("memory past validity returned: %v", got)
	}
	current.History = true
	got = ids(current)
	if !slices.Contains(got, "stale") {
		t.Fatalf("history dropped a past memory: %v", got)
	}

	one := recall.Scope{Person: "owner", Agent: "agent", Conversation: "conversation", Kinds: []recall.Kind{recall.Event}}
	got = ids(one)
	if slices.Contains(got, "elsewhere") {
		t.Fatalf("other conversation returned: %v", got)
	}
}

func indexMemory(t *testing.T, db *canon.Store, id, text string) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		return recall.Index(t.Context(), tx, "memory", id, text)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRanksPendingMaterialBeforeLimit(t *testing.T) {
	db := database(t)
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES('inline','conversation','owner','agent','person','{"kind":"tui"}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','observed','{"kind":"native"}','도서관 이전')`)
	write(t, db, `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,material_id,material_revision)
 VALUES('material','conversation','owner','agent','person','{"kind":"tui"}','2026-01-03T00:00:00Z','2026-01-03T00:00:00Z','observed','{"kind":"native"}','asset',1)`)
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Event}}
	read := func(_ context.Context, id string) (string, error) {
		if id == "material" {
			return "도서관 이후", nil
		}
		return "", nil
	}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", Limit: 1, EventText: read})
	if err != nil || len(hits) != 1 || hits[0].ID != "material" {
		t.Fatalf("newer pending material lost the limit: %+v %v", hits, err)
	}
	hits, err = recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", Limit: 2, EventText: read})
	if err != nil || len(hits) != 2 || hits[0].ID != "material" || hits[1].ID != "inline" {
		t.Fatalf("pending order: %+v %v", hits, err)
	}

	// An indexed match stays ahead of a newer unscored material hit.
	admitAt(t, db, "indexed", "conversation", "owner", "agent", "도서관", time.Unix(1700000000, 0), "")
	hits, err = recall.Match(t.Context(), db, scope, recall.Query{Text: "도서관", Limit: 1, EventText: read})
	if err != nil || len(hits) != 1 || hits[0].ID != "indexed" {
		t.Fatalf("indexed match lost to pending material: %+v %v", hits, err)
	}
}

func TestRestrictedSummaryAncestryBeforeLimit(t *testing.T) {
	db := database(t)
	base := time.Unix(1700000000, 0)
	admitAt(t, db, "protected", "private-conversation", "owner", "agent", "observatory private", base, "")
	admitAt(t, db, "safe", "public-conversation", "owner", "agent", "observatory public", base, "")
	store := compaction.New(db)
	publish := func(id, conversation, text string, children []string, event string) {
		frontier, err := store.Frontier(t.Context(), conversation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Publish(t.Context(), compaction.Proposal{Expected: frontier, ID: id, Children: children, Cover: []compaction.Coverage{{EventID: event}}, Output: compaction.Output{Text: text, Model: "fixture", PromptVersion: "1", BuiltAt: base, Dropped: "none"}}); err != nil {
			t.Fatal(err)
		}
	}
	publish("child", "private-conversation", "observatory private", nil, "protected")
	admitAt(t, db, "later", "private-conversation", "owner", "agent", "later source", base.Add(time.Second), "")
	publish("parent", "private-conversation", "observatory private", []string{"child"}, "later")
	publish("public", "public-conversation", "observatory public "+strings.Repeat("context ", 30), nil, "safe")
	protectedItem := remember(t, db, "agent", "", "observatory private", memory.NeverLeaves, "protected")
	write(t, db, `UPDATE sum_node SET text=text`)
	for _, indexed := range []bool{false, true} {
		if indexed {
			drain(t, db)
		}
		scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Summary}, ExternalModel: true}
		if ids := searchIDs(t, db, scope, "observatory", 1); !reflect.DeepEqual(ids, []string{"public"}) {
			t.Errorf("indexed=%v protected ancestor displaced visible result: %v", indexed, ids)
		}
		scope.ExternalModel = false
		if ids := searchIDs(t, db, scope, "observatory", 0); len(ids) != 3 {
			t.Errorf("local scope lost sources: %v", ids)
		}
	}
	scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Summary}, ExternalModel: true}
	hits, err := recall.Match(t.Context(), db, scope, recall.Query{Text: "no-lexical-match", Limit: 1, Vectors: func(context.Context, recall.Scope, string) ([]string, error) {
		return []string{"parent", "public"}, nil
	}})
	if err != nil || len(hits) != 1 || hits[0].ID != "public" {
		t.Fatalf("vector hook bypassed restricted ancestry: %v %v", hits, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE memory_item SET scope='private',record=json_set(record,'$.scope','private') WHERE id=?", protectedItem)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ids := searchIDs(t, db, scope, "observatory", 0); len(ids) != 3 {
		t.Fatalf("private memory was treated as never-leaves: %v", ids)
	}
	scope.Projection = true
	if ids := searchIDs(t, db, scope, "observatory", 1); !reflect.DeepEqual(ids, []string{"public"}) {
		t.Fatalf("projection exposed private ancestor: %v", ids)
	}
}

func TestSearchExcerptUsesCanonicalDerivedSpans(t *testing.T) {
	db := database(t)
	body := "observatory private-value context"
	at := time.Unix(1700000000, 0)
	event, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "flagged", ConversationID: "conversation", AgentID: "agent", PersonID: "owner", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: body, SecretSpans: []secrets.Span{{Start: len("observatory "), End: len("observatory private-value"), Sensitivity: "secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := recall.Search(t.Context(), db, nil, recall.Scope{Person: "owner", Agent: "agent", ExternalModel: true}, recall.Query{Text: "observatory"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, err)
	}
	if hits[0].Excerpt != event.Body.DerivedText() {
		t.Fatalf("excerpt differs from authorized derived text: %q", hits[0].Excerpt)
	}
}
