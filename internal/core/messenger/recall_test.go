// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/embedding"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/recall"
)

func TestMessengerExplicitRecall(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	// A chat with only another person's messages still belongs to the source owner.
	_, err := f.store.AdmitMessages(t.Context(), source, "dm-1", []messenger.Message{
		message("ko", messenger.RouteParticipant, f.now, "도서관에서 만나요"),
		message("en", messenger.RouteParticipant, f.now.Add(time.Minute), "Meet at the library"),
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := recall.Scope{Person: "person", Agent: "agent", Origin: "messenger"}
	for _, query := range []string{"도서관", "library"} {
		hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: query})
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 {
			t.Fatalf("explicit messenger %q: got %d records, want 1", query, len(hits))
		}
	}
	scope.Origin = ""
	if hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "library"}); err != nil || len(hits) != 0 {
		t.Fatalf("implicit recall = %+v, %v", hits, err)
	}
	scope.Origin, scope.SpeakerKind = "messenger", guard.Person
	if hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "library"}); err != nil || len(hits) != 0 {
		t.Fatalf("person-only recall = %+v, %v", hits, err)
	}
	scope.SpeakerKind = ""
	if _, err := f.store.DeleteChat(t.Context(), source, "dm-1"); err != nil {
		t.Fatal(err)
	}
	if hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "library"}); err != nil || len(hits) != 0 {
		t.Fatalf("deleted chat = %+v, %v", hits, err)
	}
}

func TestMessengerFiltersAndContext(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	messages := []messenger.Message{}
	for i := range 12 {
		route := messenger.RouteParticipant
		if i%2 == 0 {
			route = messenger.RoutePerson
		}
		m := message(fmt.Sprintf("m-%02d", i), route, f.now.Add(time.Duration(i)*time.Minute), fmt.Sprintf("도서관 library %02d https://example.test/?token=sensitive-value", i))
		m.Author = messenger.Author{ID: fmt.Sprintf("author-%d", i%2), Name: "Reader"}
		messages = append(messages, m)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: messages, Through: f.now, Coverage: &messenger.ReadCoverage{Interval: time.Hour, ReadWindow: 24 * time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO import_batch VALUES('fixture-batch','2026-10-24T00:00:00Z')"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO messenger_legacy_ref VALUES('murmur','fixture','message','legacy-000',?,?,?,'m-00',?,'fixture-batch','2026-10-24T00:00:00Z','{}')`, source.Platform, source.Account, "dm-1", strings.Repeat("a", 64))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := recall.Scope{Person: "person", Agent: "agent", Origin: "messenger", Platform: source.Platform, Account: source.Account, Chat: "dm-1", Author: "author-0", PersonOnly: true}
	hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "도서관"})
	if err != nil || len(hits) != 6 {
		t.Fatalf("filters: %d %v", len(hits), err)
	}
	pages, err := recall.MessengerPages(t.Context(), f.db, scope, hits, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Chat.Count != 12 || pages[0].Chat.PersonCount != 6 || pages[0].Coverage.State != "fresh" {
		t.Fatalf("pages: %+v", pages)
	}
	for _, item := range pages[0].Items {
		if strings.Contains(string(item.LegacyReference), "m-00") && (len(item.ImportedReferences) != 1 || !strings.Contains(string(item.ImportedReferences[0]), "legacy-000")) {
			t.Fatalf("import legacy reference missing: %+v", item)
		}
		if item.Revision != 1 || !item.Author.Person || !strings.Contains(string(item.LegacyReference), "m-") || strings.Contains(item.Excerpt, "sensitive-value") {
			t.Fatalf("bad evidence: %+v", item)
		}
	}
	scope.Author = "author-1"
	if hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "library"}); err != nil || len(hits) != 0 {
		t.Fatalf("incompatible filter: %d %v", len(hits), err)
	}
	scope.Author = ""
	scope.PersonOnly = false
	scope.Account = "other"
	if hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "library"}); err != nil || len(hits) != 0 {
		t.Fatalf("account: %d %v", len(hits), err)
	}
	scope.Account = source.Account
	anchor := pages[0].Items[0].ID
	first, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, "messenger:"+anchor, 0, 3, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 3 || first.NextCursor != "3" {
		t.Fatalf("first page: %+v", first)
	}
	second, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, anchor, 3, 3, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 3 || !first.Items[2].Time.Before(second.Items[0].Time) {
		t.Fatalf("second page: %+v", second)
	}
	scope.Person = "other-person"
	if _, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, anchor, 0, 3, f.now); err == nil {
		t.Fatal("another person expanded the record")
	}
	scope.Person = "person"
	chats, err := recall.MessengerChats(t.Context(), f.db, scope, "", 1, f.now.Add(48*time.Hour))
	if err != nil || len(chats) != 1 || chats[0].Coverage.State != "gap" {
		t.Fatalf("chat coverage: %+v %v", chats, err)
	}
	if _, err := f.store.DeleteChat(t.Context(), source, "dm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, anchor, 0, 3, f.now); err == nil {
		t.Fatal("deleted anchor expanded")
	}
}

func TestMessengerExpandReturnsCompleteMessage(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	// The tail sits past the search excerpt bound. A later expand offset moves
	// to another record, so the tail is reachable only in this record's body.
	body := strings.Repeat("도서관 ", 80) + "끝표시고유 https://example.test/?token=sensitive-value"
	if _, err := f.store.AdmitMessages(t.Context(), source, "dm-1", []messenger.Message{
		message("long", messenger.RoutePerson, f.now, body),
		message("next", messenger.RoutePerson, f.now.Add(time.Minute), "다음 짧은 메시지"),
	}); err != nil {
		t.Fatal(err)
	}
	scope := recall.Scope{Person: "person", Agent: "agent", Origin: "messenger"}
	hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "도서관"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %+v %v", hits, err)
	}
	if strings.Contains(hits[0].Excerpt, "끝표시고유") || utf8.RuneCountInString(hits[0].Excerpt) > recall.ExcerptRunes+1 {
		t.Fatalf("search excerpt should stay bounded: %q", hits[0].Excerpt)
	}
	page, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, hits[0].ID, 0, 1, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.NextCursor != "1" {
		t.Fatalf("page: %+v", page)
	}
	item := page.Items[0]
	if !strings.Contains(item.Excerpt, "끝표시고유") || utf8.RuneCountInString(item.Excerpt) <= recall.ExcerptRunes {
		t.Fatalf("expand truncated the message: %q (%d runes)", item.Excerpt, utf8.RuneCountInString(item.Excerpt))
	}
	if strings.Contains(item.Excerpt, "sensitive-value") {
		t.Fatalf("expand kept a secret: %q", item.Excerpt)
	}
	next, err := recall.MessengerExpand(t.Context(), f.db, nil, scope, hits[0].ID, 1, 1, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || strings.Contains(next.Items[0].Excerpt, "끝표시고유") || !strings.Contains(next.Items[0].Excerpt, "다음 짧은 메시지") {
		t.Fatalf("next page: %+v", next)
	}
}

func TestMessengerWindowsConsentAndRemoval(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	other := source
	other.Account = "off-account"
	if err := f.store.Connect(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertChats(t.Context(), other, []messenger.Chat{{ID: "dm-1", Name: "Off", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitMessages(t.Context(), other, "dm-1", []messenger.Message{message("off", messenger.RoutePerson, f.now, "DO-NOT-SEND private chat")}); err != nil {
		t.Fatal(err)
	}
	messages := []messenger.Message{}
	for i := range 12 {
		at := f.now.Add(time.Duration(i) * time.Minute)
		if i == 11 {
			at = at.Add(6 * time.Minute)
		}
		messages = append(messages, message(fmt.Sprintf("m-%02d", i), messenger.RoutePerson, at, fmt.Sprintf("window record %02d", i)))
	}
	messages[0].Text += " https://example.test/?token=window-secret-value"
	if _, err := f.store.AdmitMessages(t.Context(), source, "dm-1", messages); err != nil {
		t.Fatal(err)
	}
	index := recall.New(f.db, nil)
	drain := func() {
		t.Helper()
		for range 30 {
			n, err := index.DrainBatch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 && count(t, f.db, "SELECT count(*) FROM messenger_window_pending") == 0 {
				return
			}
		}
		t.Fatal("window queue did not drain")
	}
	drain()
	if n := count(t, f.db, "SELECT count(*) FROM messenger_window"); n != 0 {
		t.Fatalf("embedding off created %d windows", n)
	}
	var texts []string
	var textMu sync.Mutex
	recorded := func() []string { textMu.Lock(); defer textMu.Unlock(); return append([]string{}, texts...) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		textMu.Lock()
		texts = append(texts, in.Input...)
		textMu.Unlock()
		data := []map[string]any{}
		for i := range in.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	adapter, err := embedding.New(embedding.Config{Endpoint: server.URL, Model: "fixture", Dimension: 2}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := recall.NewVectors(t.Context(), f.db, nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	finish := func() {
		t.Helper()
		for range 30 {
			if _, err := vectors.DrainBatch(t.Context()); err != nil {
				t.Fatal(err)
			}
			if count(t, f.db, "SELECT ready FROM search_index_config WHERE name='recall'") == 1 {
				return
			}
		}
		t.Fatal("vectors did not drain")
	}
	finish()
	if len(recorded()) != 0 {
		t.Fatalf("embedding off sent %v", recorded())
	}
	if err := f.store.SetEmbedding(t.Context(), "person", source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetSwitches(t.Context(), "person", source.Platform, source.Account, "dm-1", messenger.Switches{Sync: true, Embed: true}); err != nil {
		t.Fatal(err)
	}
	drain()
	finish()
	if n := count(t, f.db, "SELECT count(*) FROM messenger_window"); n != 3 {
		t.Fatalf("windows=%d, want 10 + 1 + gap + 1", n)
	}
	if n := count(t, f.db, "SELECT max(n) FROM (SELECT count(*) n FROM cite JOIN messenger_window w ON w.id=cite.view_id GROUP BY w.id)"); n != 10 {
		t.Fatalf("largest window=%d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_text WHERE rowid IN (SELECT d.id FROM recall_doc d JOIN messenger_window w ON w.id=d.source_id)"); n != 0 {
		t.Fatal("window has index text")
	}
	joined := strings.Join(recorded(), "|")
	if strings.Contains(joined, "DO-NOT-SEND") || !strings.Contains(joined, "\nwindow record 02") || strings.Contains(joined, "window-secret-value") {
		t.Fatalf("endpoint texts: %q", joined)
	}
	scope := recall.Scope{Person: "person", Agent: "agent", Origin: "messenger"}
	// Remove individual event vectors: semantic results must come from windows.
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM recall_vec WHERE rowid IN (SELECT id FROM recall_doc WHERE kind='event')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "semantic paraphrase", Vectors: vectors.Query, Limit: 20})
	if err != nil || len(hits) != 12 {
		t.Fatalf("window hits=%d %v", len(hits), err)
	}
	edited := messages[0]
	edited.Edited = f.now.Add(time.Hour)
	edited.Text = "replacement telescope"
	if _, err := f.store.AdmitMessages(t.Context(), source, "dm-1", []messenger.Message{edited}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_window"); n != 0 {
		t.Fatal("edit retained old window")
	}
	drain()
	finish()
	updated, err := recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "telescope"})
	if err != nil || len(updated) != 1 {
		t.Fatalf("updated record: %+v %v", updated, err)
	}
	pages, err := recall.MessengerPages(t.Context(), f.db, scope, updated, f.now)
	if err != nil || pages[0].Items[0].Revision != 2 {
		t.Fatalf("updated revision: %+v %v", pages, err)
	}
	// Revoking consent invalidates both stored vectors and queued window work.
	if err := f.store.SetEmbedding(t.Context(), "person", source.Platform, source.Account, false); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_vec"); n != 0 {
		t.Fatalf("revocation retained %d vectors", n)
	}
	sent := len(recorded())
	drain()
	finish()
	if len(recorded()) != sent {
		t.Fatal("revoked source reached endpoint")
	}
	if err := f.store.SetEmbedding(t.Context(), "person", source.Platform, source.Account, true); err != nil {
		t.Fatal(err)
	}
	drain()
	finish()
	if _, err := f.store.DeleteChat(t.Context(), source, "dm-1"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_vec"); n != 0 {
		t.Fatalf("deleted chat retained %d vectors", n)
	}
	hits, err = recall.Search(t.Context(), f.db, nil, scope, recall.Query{Text: "semantic paraphrase", Vectors: vectors.Query})
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted semantic hits=%d %v", len(hits), err)
	}
}
