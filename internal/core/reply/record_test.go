// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func database(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()}
	return openDB(t, cfg), cfg
}
func openDB(t *testing.T, cfg canon.Config) *canon.Store {
	t.Helper()
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return db
}
func recording() reply.Recording {
	return reply.Recording{
		Source: ledger.Input{ID: "reply", ConversationID: "conversation", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"},
			OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), RecordedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC), TimeQuality: "observed"},
		CardEventID: "card-event", ResultID: "result", TurnID: "turn", Title: "Answer", Summary: "Full answer",
	}
}

func TestRecordFullReplyAndPreviewSurviveRestart(t *testing.T) {
	for _, full := range []string{"A short reply.", "끝. " + strings.Repeat("가", 301), "one.\ntwo.\nthree.\nfour.\nfive.\nsix."} {
		t.Run(full[:min(8, len(full))], func(t *testing.T) {
			db, cfg := database(t)
			store := ledger.New(db, nil)
			f := filter(t, config())
			v, err := f.Push(full)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := f.Record(t.Context(), store, recording())
			if err != nil {
				t.Fatal(err)
			}
			if saved.Reply.Body.Text() != full || saved.Reply.SpeakerKind != guard.Agent {
				t.Fatalf("reply=%+v", saved.Reply)
			}
			if (saved.Card != nil) != v.Cut {
				t.Fatalf("card=%+v", saved.Card)
			}
			if _, err := f.Push("late"); !errors.Is(err, reply.ErrClosed) {
				t.Fatalf("late delta accepted: %v", err)
			}
			if _, err := f.Record(t.Context(), store, recording()); err != nil {
				t.Fatalf("repeat record: %v", err)
			}
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			store = ledger.New(openDB(t, cfg), nil)
			source, err := store.Get(t.Context(), "reply")
			if err != nil || source.Body.Text() != full {
				t.Fatalf("source=%+v %v", source, err)
			}
			card, err := store.Get(t.Context(), "card-event")
			if !v.Cut {
				if !errors.Is(err, ledger.ErrNotFound) {
					t.Fatalf("unexpected card: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var result reply.Card
			if err := json.Unmarshal([]byte(card.Body.Text()), &result); err != nil {
				t.Fatal(err)
			}
			if result.Type != "document_draft" || result.Status != "preview" || result.Text != full || result.Body.Blocks[0].Text != full || result.ResultID != "result" || result.Revision != 1 || result.Provenance.TurnID != "turn" || card.ParentID != "reply" {
				t.Fatalf("card=%+v event=%+v", result, card)
			}
		})
	}
}

type failWrite struct {
	*canon.Store
	writes int
	failAt int
}

func (s *failWrite) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	s.writes++
	if s.writes == s.failAt {
		return errors.New("injected commit failure")
	}
	return s.Store.Write(ctx, fn)
}
func TestRecordFailureRetainsSourceAndRetriesPreview(t *testing.T) {
	for _, at := range []int{1, 2} {
		t.Run(string(rune('0'+at)), func(t *testing.T) {
			db, _ := database(t)
			dbFail := &failWrite{Store: db, failAt: at}
			store := ledger.New(dbFail, nil)
			f := filter(t, config())
			full := "Done! " + strings.Repeat("가", 301)
			_, _ = f.Push(full)
			if _, err := f.Record(t.Context(), store, recording()); err == nil {
				t.Fatal("failed commit acknowledged")
			}
			_, err := store.Get(t.Context(), "card-event")
			if !errors.Is(err, ledger.ErrNotFound) {
				t.Fatalf("partial card: %v", err)
			}
			source, err := store.Get(t.Context(), "reply")
			if at == 2 && (err != nil || source.Body.Text() != full) {
				t.Fatalf("lost full source: %v", err)
			}
			saved, err := f.Record(t.Context(), store, recording())
			if err != nil || saved.Card == nil {
				t.Fatalf("retry=%+v %v", saved, err)
			}
			changed := recording()
			changed.ResultID = "different"
			if _, err := f.Record(t.Context(), store, changed); !errors.Is(err, reply.ErrClosed) {
				t.Fatalf("changed retry accepted: %v", err)
			}
		})
	}
}

// The production composition layer supplies this ledger/materials boundary.
// Both stores below are real, so a restart verifies bytes as well as row IDs.
type materialStore struct{ store *assets.Store }

func (m materialStore) Save(ctx context.Context, body secrets.Masked) (ledger.Material, error) {
	token, err := m.store.StageImport(ctx, strings.NewReader(body.Text()))
	if err != nil {
		return ledger.Material{}, err
	}
	a, err := m.store.Handover(ctx, token, "reply-body", assets.Lina, assets.Actor{PersonID: "owner", AgentID: "agent"})
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m materialStore) Read(ctx context.Context, ref ledger.Material) (string, error) {
	data, err := m.store.Read(ctx, ref.ID, ref.Revision)
	return string(data), err
}
func TestLargeReplyAndMaskedCardSurviveRestart(t *testing.T) {
	db, cfg := database(t)
	root := filepath.Dir(cfg.Path)
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	f := filter(t, config())
	full := "Done! " + strings.Repeat("가", ledger.InlineBytes) + " private-value"
	_, err = f.Push(full)
	if err != nil {
		t.Fatal(err)
	}
	r := recording()
	r.Source.SecretSpans = []secrets.Span{{Start: len(full) - len("private-value"), End: len(full)}}
	saved, err := f.Record(t.Context(), ledger.New(db, materialStore{a}), r)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Reply.Material.ID == "" || saved.Card == nil || saved.Card.Material.ID == "" {
		t.Fatal("large bodies not in materials")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	db = openDB(t, cfg)
	a, err = assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	store := ledger.New(db, materialStore{a})
	for _, id := range []string{"reply", "card-event"} {
		event, err := store.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(event.Body.Text(), "private-value") || !strings.Contains(event.Body.Text(), secret.Placeholder) || len(event.Spans) == 0 || strings.Contains(event.Body.DerivedText(), secret.Placeholder) {
			t.Fatalf("masking lost in %s", id)
		}
	}
	event, err := store.Get(t.Context(), "card-event")
	if err != nil {
		t.Fatal(err)
	}
	var card reply.Card
	if err := json.Unmarshal([]byte(event.Body.Text()), &card); err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSuffix(full, "private-value") + secret.Placeholder
	if card.Text != want || card.Body.Blocks[0].Text != want {
		t.Fatal("large card content changed")
	}
}

func TestRecordEmptySlicesAreStable(t *testing.T) {
	db, _ := database(t)
	store := ledger.New(db, nil)
	f := filter(t, config())
	_, _ = f.Push("Short.")
	r := recording()
	r.Source.Relations = []ledger.Relation{}
	r.Source.Tags = []secrets.Span{}
	if _, err := f.Record(t.Context(), store, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Record(t.Context(), store, r); err != nil {
		t.Fatal(err)
	}
}
