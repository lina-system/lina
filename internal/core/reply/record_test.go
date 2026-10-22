// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
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
			var result reply.CardReference
			if err := json.Unmarshal([]byte(card.Body.Text()), &result); err != nil {
				t.Fatal(err)
			}
			if result.Type != "document_draft" || result.Status != "preview" || result.SourceEvent != source.ID || card.SpeakerKind != guard.System || strings.Contains(card.Body.Text(), full) || result.ResultID != "result" || result.Revision != 1 || result.Provenance.TurnID != "turn" || card.ParentID != "reply" {
				t.Fatalf("card=%+v event=%+v", result, card)
			}
		})
	}
}

type failWrite struct {
	*canon.Store
	writes int
	after  bool
}

func (s *failWrite) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	s.writes++
	if s.writes != 1 {
		return s.Store.Write(ctx, fn)
	}
	if !s.after {
		return errors.New("injected commit failure")
	}
	return s.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected rollback")
	})
}
func TestRecordFailureRollsBackPairAndRetries(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			db, _ := database(t)
			dbFail := &failWrite{Store: db, after: after}
			store := ledger.New(dbFail, nil)
			f := filter(t, config())
			full := "Done! " + strings.Repeat("가", 301)
			_, _ = f.Push(full)
			if _, err := f.Record(t.Context(), store, recording()); err == nil {
				t.Fatal("failed commit acknowledged")
			}
			for _, id := range []string{"reply", "card-event"} {
				if _, err := store.Get(t.Context(), id); !errors.Is(err, ledger.ErrNotFound) {
					t.Fatalf("partial %s: %v", id, err)
				}
			}
			saved, err := f.Record(t.Context(), store, recording())
			if err != nil || saved.Card == nil {
				t.Fatalf("retry=%+v %v", saved, err)
			}
			if dbFail.writes != 2 {
				t.Fatalf("reply/card need one atomic write, got %d attempts", dbFail.writes)
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
type materialStore struct {
	store  *assets.Store
	writer *assets.MaterialWriter
}

func materials(t *testing.T, store *assets.Store) materialStore {
	t.Helper()
	writer, err := assets.OpenMaterialWriter(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	return materialStore{store, writer}
}

type materialWrite struct{ *assets.PreparedMaterial }

func (p materialWrite) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m materialStore) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "reply-body", assets.Actor{PersonID: "owner", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return materialWrite{p}, nil
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
	saved, err := f.Record(t.Context(), ledger.New(db, materials(t, a)), r)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Reply.Material.ID == "" || saved.Card == nil || saved.Card.Material.ID != "" {
		t.Fatal("source must use materials; reference card must remain inline")
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
	store := ledger.New(db, materials(t, a))
	for _, id := range []string{"reply"} {
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
	var card reply.CardReference
	if err := json.Unmarshal([]byte(event.Body.Text()), &card); err != nil {
		t.Fatal(err)
	}
	if card.SourceEvent != "reply" || strings.Contains(event.Body.Text(), secret.Placeholder) {
		t.Fatal("card must reference the masked source")
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

func TestRecordedPreviewPreservesJSONMaskingAcrossRestart(t *testing.T) {
	for _, tail := range []string{
		"Enter the password: ",
		`The password="synthetic-private" is set.`,
		`{"password":"synthetic-private"}`,
	} {
		t.Run(tail, func(t *testing.T) {
			db, cfg := database(t)
			f := filter(t, config())
			full := "Done. " + strings.Repeat("가", 301) + " private-value " + tail
			if _, err := f.Push(full); err != nil {
				t.Fatal(err)
			}
			r := recording()
			start := strings.Index(full, "private-value")
			r.Source.SecretSpans = []secrets.Span{{Start: start, End: start + len("private-value")}}
			want, err := secrets.Mask(full, r.Source.SecretSpans...)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := f.Record(t.Context(), ledger.New(db, nil), r)
			if err != nil || saved.Card == nil {
				t.Fatalf("recorded card: %+v %v", saved, err)
			}
			check := func(event ledger.Event) {
				t.Helper()
				for _, text := range []string{event.Body.Text(), event.Body.DerivedText()} {
					if !json.Valid([]byte(text)) || strings.Contains(text, "private-value") || strings.Contains(text, "synthetic-private") {
						t.Fatalf("invalid or unmasked card JSON: %q", text)
					}
				}
				var card reply.CardReference
				if err := json.Unmarshal([]byte(event.Body.Text()), &card); err != nil {
					t.Fatal(err)
				}
				if card.SourceEvent != r.Source.ID || saved.Reply.Body.Text() != want.Text() {
					t.Fatalf("card content changed: %+v", card)
				}
				if len(event.Spans) != 0 || strings.Contains(event.Body.DerivedText(), secret.Placeholder) {
					t.Fatal("reference card retained source secret spans")
				}
			}
			check(*saved.Card)
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			card, err := ledger.New(openDB(t, cfg), nil).Get(t.Context(), r.CardEventID)
			if err != nil {
				t.Fatal(err)
			}
			check(card)
		})
	}
}

func TestRecordedPreviewDoesNotDuplicateModelHistory(t *testing.T) {
	db, cfg := database(t)
	f := filter(t, config())
	full := "Done. " + strings.Repeat("가", 301)
	if _, err := f.Push(full); err != nil {
		t.Fatal(err)
	}
	r := recording()
	saved, err := f.Record(t.Context(), ledger.New(db, nil), r)
	if err != nil || saved.Card == nil {
		t.Fatalf("recorded card: %+v %v", saved, err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := ledger.New(openDB(t, cfg), nil)
	var events []ledger.Event
	for _, id := range []string{r.Source.ID, r.CardEventID} {
		event, err := store.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	items, err := assemble.History(events, r.Source.ConversationID, r.Source.AgentID)
	if err != nil || len(items) != 1 {
		t.Fatalf("model history contains %d items, want one full reply: %v", len(items), err)
	}
	var item struct{ Role, Content string }
	if err := json.Unmarshal(items[0], &item); err != nil {
		t.Fatal(err)
	}
	if item.Role != "assistant" || item.Content != full {
		t.Fatalf("model history lost the original full reply: %+v", item)
	}
}

func TestLargeReplyBatchRollbackRemovesBothMaterials(t *testing.T) {
	db, cfg := database(t)
	a, err := assets.Open(t.Context(), filepath.Dir(cfg.Path), db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	writer := materials(t, a)
	failing := &failWrite{Store: db, after: true}
	f := filter(t, config())
	_, _ = f.Push("Done! " + strings.Repeat("가", ledger.InlineBytes))
	store := ledger.New(failing, writer)
	if _, err := f.Record(t.Context(), store, recording()); err == nil {
		t.Fatal("rollback acknowledged")
	}
	for _, id := range []string{"reply", "card-event"} {
		if _, err := store.Get(t.Context(), id); !errors.Is(err, ledger.ErrNotFound) {
			t.Fatalf("partial %s: %v", id, err)
		}
	}
	manifest, err := a.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 0 {
		t.Fatalf("unreferenced materials retained: %v %v", manifest, err)
	}
	if _, err := f.Record(t.Context(), store, recording()); err != nil {
		t.Fatal(err)
	}
	manifest, err = a.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 1 {
		t.Fatalf("expected one source material: %v %v", manifest, err)
	}
}
