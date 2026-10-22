// SPDX-License-Identifier: Apache-2.0
package ledger_test

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestKoreanSearch(t *testing.T) {
	var corpus struct {
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
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	db, cfg := database(t)
	s := ledger.New(db, nil)
	for _, doc := range corpus.Documents {
		in := input(doc.ID)
		in.Body = doc.Body
		if doc.Origin == "native" {
			admit(t, s, guard.EntryOwner, in)
			continue
		}
		// The importer owns admission of historical events; seed its committed
		// representation to exercise the same index backfill as an old database.
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event
			 (id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
			 VALUES(?, 'imported', 'owner', 'agent', 'person', '{"kind":"app"}', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 'recorded', '{"kind":"import","locator":"fixture:history"}', ?)`, doc.ID, doc.Body)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	other := input("other-agent")
	other.AgentID, other.ConversationID, other.Body = "other", "other-conversation", "도서관에서 공부를 했다."
	admit(t, s, guard.EntryOwner, other)
	for _, reopened := range []bool{false, true} {
		if reopened {
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			db = open(t, cfg)
			s = ledger.New(db, nil)
		}
		for _, query := range corpus.Queries {
			t.Run(query.Name, func(t *testing.T) {
				hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query.Text})
				if err != nil {
					t.Fatal(err)
				}
				ids := []string{}
				for _, hit := range hits {
					ids = append(ids, hit.ID)
					if hit.AgentID != "agent" {
						t.Fatalf("foreign hit: %+v", hit)
					}
					if hit.ConversationID == "imported" && hit.Origin.Kind != "import" {
						t.Fatal("lost import provenance")
					}
				}
				sort.Strings(ids)
				if !reflect.DeepEqual(ids, query.IDs) {
					t.Fatalf("ids=%v, want %v", ids, query.IDs)
				}
			})
		}
	}
}

func TestKoreanSearchExcludesSecretSpansAndRollbacks(t *testing.T) {
	db, _ := database(t)
	s := ledger.New(db, nil)
	in := input("secret")
	in.Body = "도서관 공부 공비밀부"
	in.SecretSpans = []secrets.Span{{Start: 0, End: len("도서관 공부")}, {Start: len("도서관 공부 공"), End: len("도서관 공부 공비밀")}}
	admit(t, s, guard.EntryOwner, in)
	failure := errors.New("rollback")
	failed := ledger.New(failingCanon{Store: db, failure: failure}, nil)
	in = input("rolled-back")
	in.Body = "도서관에서 공부했다."
	if _, err := failed.Admit(t.Context(), guard.EntryOwner, in); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, query := range []string{"도서관", "공부", "비밀", "REDACTED"} {
		hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query})
		if err != nil || len(hits) != 0 {
			t.Fatalf("query %q: hits=%v, err=%v", query, hits, err)
		}
	}
	if _, err := s.Search(t.Context(), ledger.SearchQuery{Text: "공부"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("unscoped query: %v", err)
	}
}

func TestKoreanSearchMigrationAndMetadata(t *testing.T) {
	all := canon.Migrations()
	index := -1
	for i, migration := range all {
		if strings.Contains(migration.SQL, "CREATE TABLE ledger_search_text") {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("missing appended search migration")
	}
	db, cfg := database(t, all[:index])
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event
		 (id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
		 VALUES('old', 'old-conversation', 'owner', 'agent', 'person', '{"kind":"app"}', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 'recorded', '{"kind":"import","locator":"fixture:old"}', '도서관에서 공부했다.')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = all, cfg.Epoch+1
	db = open(t, cfg)
	s := ledger.New(db, nil)
	for _, query := range []string{"도서관", "공부"} {
		hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query})
		if err != nil || len(hits) != 1 || hits[0].ID != "old" {
			t.Fatalf("backfill: %v %v", hits, err)
		}
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var tokenizer, fallback string
		if err := tx.QueryRowContext(t.Context(), "SELECT tokenizer,fallback FROM search_index_config WHERE name='ledger'").Scan(&tokenizer, &fallback); err != nil {
			return err
		}
		if tokenizer != "trigram" || fallback != "hangul_bigram" {
			t.Fatalf("metadata: %q %q", tokenizer, fallback)
		}
		var pending int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ledger_search_pending").Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			t.Fatal("backfill did not drain pending records")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKoreanSearchFiltersBeforeLimitAndKeepsReplayOptIn(t *testing.T) {
	db, _ := database(t)
	s := ledger.New(db, nil)
	for _, id := range []string{"foreign", "original", "replay"} {
		in := input(id)
		in.Body = "도서관에서 공부를 했다."
		if id == "foreign" {
			in.AgentID = "other"
			in.OccurredAt = in.OccurredAt.Add(time.Hour)
		}
		if id == "replay" {
			in.ReplayOf = "original"
			in.OccurredAt = in.OccurredAt.Add(time.Minute)
		}
		admit(t, s, guard.EntryOwner, in)
	}
	for _, text := range []string{"도서관", "공부"} {
		q := ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: text, Limit: 1, Origin: "native", SpeakerKind: guard.Person}
		hits, err := s.Search(t.Context(), q)
		if err != nil || len(hits) != 1 || hits[0].ID != "original" {
			t.Fatalf("filters before limit: %v %v", hits, err)
		}
		q.IncludeReplay = true
		hits, err = s.Search(t.Context(), q)
		if err != nil || len(hits) != 1 || hits[0].ID != "replay" {
			t.Fatalf("replay opt-in: %v %v", hits, err)
		}
		q.Since = input("").OccurredAt.Add(time.Hour)
		if hits, err := s.Search(t.Context(), q); err != nil || len(hits) != 0 {
			t.Fatalf("time filter: %v %v", hits, err)
		}
	}
}

func TestKoreanSearchPublishesOnlyAtCommit(t *testing.T) {
	db, _ := database(t)
	s := ledger.New(db, nil)
	observed := false
	observer := observingCanon{db, func() {
		observed = true
		for _, query := range []string{"도서관", "공부"} {
			hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query})
			if err != nil || len(hits) != 0 {
				t.Fatalf("uncommitted index visible: %v %v", hits, err)
			}
		}
	}}
	in := input("commit")
	in.Body = "도서관에서 공부한다."
	admit(t, ledger.New(observer, nil), guard.EntryOwner, in)
	for _, query := range []string{"도서관", "공부"} {
		hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query})
		if !observed || err != nil || len(hits) != 1 || hits[0].ID != in.ID {
			t.Fatalf("committed index missing: %v %v", hits, err)
		}
	}
}

func TestKoreanSearchIsolatesPeopleAndTheirReplies(t *testing.T) {
	db, _ := database(t)
	s := ledger.New(db, nil)
	for _, person := range []string{"owner", "other-person"} {
		in := input(person)
		in.PersonID, in.ConversationID = person, person+"-conversation"
		in.Body = "도서관에서 공부를 한다."
		admit(t, s, guard.EntryOwner, in)
		in.ID, in.PersonID = person+"-reply", ""
		admit(t, s, guard.EntryAgent, in)
		in = input(person + "-ambiguous")
		in.PersonID, in.ConversationID, in.Body = person, "ambiguous", "도서관에서 공부한다."
		admit(t, s, guard.EntryOwner, in)
	}
	orphan := input("orphan-reply")
	orphan.PersonID, orphan.ConversationID, orphan.Body = "", "orphan", "도서관에서 공부한다."
	admit(t, s, guard.EntryAgent, orphan)
	for _, person := range []string{"owner", "other-person"} {
		for _, text := range []string{"도서관", "공부"} {
			hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: person, Text: text})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, hit := range hits {
				ids = append(ids, hit.ID)
			}
			sort.Strings(ids)
			if want := []string{person, person + "-reply"}; !reflect.DeepEqual(ids, want) {
				t.Fatalf("person %q: ids=%v want=%v", person, ids, want)
			}
		}
	}
	if _, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", Text: "공부"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("missing person scope: %v", err)
	}
	// A pending private source must not even be read for somebody else's
	// search: missing foreign material cannot fail or disclose that query.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event
		 (id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,material_id,material_revision)
		 VALUES('foreign-pending','foreign-pending','other-person','agent','person','{"kind":"app"}',
		 '2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','recorded','{"kind":"import"}','missing-material',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: "공부", Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].ID != "owner" {
		t.Fatalf("foreign backfill affected owner: %v %v", hits, err)
	}
}

func TestSearchReturnsLongReplyOnceIncludingLegacyCards(t *testing.T) {
	db, _ := database(t)
	store := ledger.New(db, nil)
	owner := input("question")
	owner.Body = "Please explain the subject."
	admit(t, store, guard.EntryOwner, owner)
	source := input("source")
	source.PersonID = ""
	source.Body = "Synthetic long reply with a unique recall needle."
	admit(t, store, guard.EntryAgent, source)
	card := input("legacy-card")
	card.PersonID = ""
	card.ScaffoldKind = "result"
	card.ParentID = source.ID
	card.Relations = []ledger.Relation{{Kind: "view_of", TargetID: source.ID}}
	card.Body = `{"type":"document_draft","text":"` + source.Body + `"}`
	admit(t, store, guard.EntryAgent, card)
	hits, err := store.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: "unique recall needle", Limit: 1})
	if err != nil || len(hits) != 1 || hits[0].ID != source.ID {
		t.Fatalf("recall: %+v %v", hits, err)
	}
	hits, err = store.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: "unique recall needle"})
	if err != nil || len(hits) != 1 || hits[0].ID != source.ID {
		t.Fatalf("recall: %+v %v", hits, err)
	}
}
