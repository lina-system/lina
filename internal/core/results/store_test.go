// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

// A counting writer makes failures/concurrency deterministic; the disk writer is
// exercised separately with real Git object verification.
type testWriter struct {
	mu    sync.Mutex
	count int
	err   error
}

func (w *testWriter) Prepare(_ context.Context, s result.Scope, r result.Result, _ result.Destination) (PreparedSave, error) {
	return func(ctx context.Context, ref result.SavedRef) error { return w.Save(ctx, s, r, ref) }, nil
}
func (w *testWriter) Save(_ context.Context, _ result.Scope, _ result.Result, ref result.SavedRef) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	return w.err
}
func setup(t *testing.T) (*Store, *canon.Store, canon.Config, *testWriter) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	ms := canon.Migrations()
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ms}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	w := &testWriter{}
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w, TextRunes: 12})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, cfg, w
}

var scope = result.Scope{PersonID: "person", AgentID: "agent", ConversationID: "conversation", TurnID: "turn"}

func proposal() result.PresentRequest {
	return result.PresentRequest{Type: result.DraftType, Title: "Notes", Summary: "A short draft.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"A complete document with more than twelve characters."}],"from_summary":null}`)}
}
func present(t *testing.T, s *Store) result.Card {
	t.Helper()
	c, err := s.Present(t.Context(), scope, proposal())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func count(t *testing.T, db *canon.Store, table string) int {
	t.Helper()
	var n int
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func TestMalformedNeverAppends(t *testing.T) {
	s, db, _, _ := setup(t)
	p := proposal()
	p.Body = json.RawMessage(`{"blocks":[],"from_summary":null}`)
	if _, err := s.Present(t.Context(), scope, p); !errors.Is(err, result.ErrMalformed) {
		t.Fatal(err)
	}
	if count(t, db, "conv_event") != 0 || count(t, db, "result_heads") != 0 {
		t.Fatal("invalid result became conversation content")
	}
}
func TestReadRevisionAndStaleReplacement(t *testing.T) {
	s, db, _, _ := setup(t)
	c := present(t, s)
	if len([]rune(c.Text)) != 12 {
		t.Fatal(c)
	}
	r, err := s.Read(t.Context(), scope, result.ReadRequest{ID: c.ID})
	if err != nil || r.Revision != 1 || len(r.Body) == 0 {
		t.Fatal(r, err)
	}
	p := proposal()
	p.ID = c.ID
	p.BaseRevision = r.Revision
	p.Title = "Changed"
	if _, err := s.Present(t.Context(), scope, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Present(t.Context(), scope, p); !errors.Is(err, ErrStaleRevision) {
		t.Fatal(err)
	}
	if count(t, db, "conv_event") != 2 {
		t.Fatal("stale revision appended")
	}
	foreign := scope
	foreign.PersonID = "another-person"
	if _, err := s.Read(t.Context(), foreign, result.ReadRequest{ID: c.ID}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestDoubleSaveAndReopen(t *testing.T) {
	s, db, cfg, w := setup(t)
	c := present(t, s)
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
	var wg sync.WaitGroup
	receipts := make(chan result.SaveReceipt, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { r, err := s.Save(t.Context(), scope, req); receipts <- r; errs <- err })
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var saved *result.SavedRef
	for r := range receipts {
		if r.State != "applied" && r.State != "unknown" {
			t.Fatal(r)
		}
		if r.SavedAs != nil {
			saved = r.SavedAs
		}
	}
	if saved == nil || w.count != 1 || count(t, db, "result_saves") != 1 {
		t.Fatal("double save", saved, w.count)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err = New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.Save(t.Context(), scope, req)
	if err != nil || receipt.State != "applied" || receipt.SavedAs.ID != saved.ID || w.count != 1 {
		t.Fatal(receipt, err, w.count)
	}
	p := proposal()
	p.ID = c.ID
	p.BaseRevision = 1
	if _, err := s.Present(t.Context(), scope, p); !errors.Is(err, ErrOutOfScope) {
		t.Fatal(err)
	}
	r, err := s.Read(t.Context(), scope, result.ReadRequest{ID: c.ID})
	if err != nil || r.Status != "saved" || r.SavedAs.ID != saved.ID {
		t.Fatal(r, err)
	}
}
func TestSaveStaleAndUnsupported(t *testing.T) {
	s, _, _, w := setup(t)
	c := present(t, s)
	p := proposal()
	p.ID = c.ID
	p.BaseRevision = 1
	if _, err := s.Present(t.Context(), scope, p); err != nil {
		t.Fatal(err)
	}
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
	if _, err := s.Save(t.Context(), scope, req); !errors.Is(err, ErrStaleRevision) {
		t.Fatal(err)
	}
	req.Revision = 2
	req.Destination.Kind = "network"
	if _, err := s.Save(t.Context(), scope, req); !errors.Is(err, ErrOutOfScope) {
		t.Fatal(err)
	}
	if w.count != 0 {
		t.Fatal("refused save dispatched")
	}
}
func TestUnknownSaveNeverRepeats(t *testing.T) {
	s, _, _, w := setup(t)
	c := present(t, s)
	w.err = errors.New("interrupted publication")
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
	r, err := s.Save(t.Context(), scope, req)
	if err == nil || r.State != "unknown" {
		t.Fatal(r, err)
	}
	r, err = s.Save(t.Context(), scope, req)
	if err != nil || r.State != "unknown" || w.count != 1 {
		t.Fatal(r, err, w.count)
	}
}

func TestLargePreviewStaysInLedgerAndMasksBeforeReturn(t *testing.T) {
	s, db, _, _ := setup(t)
	p := proposal()
	text := strings.Repeat("Long content. ", 2000) + " password=example-secret"
	body := result.Draft{Blocks: []result.Block{{Kind: "paragraph", Text: text}}}
	p.Body, _ = json.Marshal(body)
	c, err := s.Present(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Read(t.Context(), scope, result.ReadRequest{ID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.Body), "example-secret") || strings.Contains(r.Text, "example-secret") || len(r.Text) < 16000 {
		t.Fatal("mask or complete text missing")
	}
	var inline int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE body IS NOT NULL AND material_id IS NULL").Scan(&inline)
	})
	if err != nil || inline != 1 {
		t.Fatal(inline, err)
	}
}
