// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/result"
)

func TestRejectedContainerDoesNotClaimPreview(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := OpenRecords(root, db, nil, func(context.Context, result.Scope, result.Destination) (string, error) { return "", ErrOutOfScope })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	c := present(t, s)
	r, err := s.Save(t.Context(), scope, result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "project", Container: "deleted"}})
	if !errors.Is(err, ErrOutOfScope) || r.State != "refused" || count(t, db, "result_saves") != 0 {
		t.Fatalf("invalid container claimed preview: %+v %v", r, err)
	}
	p := proposal()
	p.ID = c.ID
	p.BaseRevision = 1
	c, err = s.Present(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.Save(t.Context(), scope, result.SaveRequest{ID: c.ID, Revision: c.Revision, Destination: result.Destination{Kind: "resource"}})
	if err != nil || r.State != "applied" {
		t.Fatalf("valid save after refusal: %+v %v", r, err)
	}
}

func TestResolvedDestinationIsNotLookedUpAgainAfterClaim(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	os.Chmod(root, 0700)
	calls := 0
	w, err := OpenRecords(root, db, nil, func(context.Context, result.Scope, result.Destination) (string, error) {
		calls++
		if calls > 1 {
			return "", ErrOutOfScope
		}
		return "projects/example", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	c := present(t, s)
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "project", Container: "project"}}
	for range 2 {
		r, err := s.Save(t.Context(), scope, req)
		if err != nil || r.State != "applied" {
			t.Fatal(r, err)
		}
	}
	if calls != 1 {
		t.Fatal("destination re-resolved", calls)
	}
	if _, err := os.Stat(filepath.Join(root, "versions/records/projects/example/notes.md")); err != nil {
		t.Fatal(err)
	}
}

type failPublicationCanon struct {
	Canon
	root    string
	failure error
}

func (f failPublicationCanon) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	if _, err := os.Stat(filepath.Join(f.root, "versions/records/.git/refs/heads/records")); err == nil {
		return f.failure
	}
	return f.Canon.Write(ctx, fn)
}
func TestReopenRecoversPublishedRecordBeforeCheckpoint(t *testing.T) {
	t.Parallel()
	_, db, _, _ := setup(t)
	root := t.TempDir()
	os.Chmod(root, 0700)
	injected := errors.New("interrupted checkpoint")
	w, err := OpenRecords(root, failPublicationCanon{db, root, injected}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	c := present(t, s)
	req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
	receipt, err := s.Save(t.Context(), scope, req)
	if !errors.Is(err, injected) || receipt.State != "unknown" {
		t.Fatal(receipt, err)
	}
	w.Close()
	w, err = OpenRecords(root, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s, err = New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Save(t.Context(), scope, req)
	if err != nil || again.State != "unknown" {
		t.Fatal("original receipt changed or redispatched", again, err)
	}
	next := present(t, s)
	receipt, err = s.Save(t.Context(), scope, result.SaveRequest{ID: next.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}})
	if err != nil || receipt.State != "applied" {
		t.Fatalf("interrupted publication blocked later save: %+v %v", receipt, err)
	}
	files, err := os.ReadDir(filepath.Join(root, "versions/records/resources"))
	if err != nil || len(files) != 2 {
		t.Fatal(files, err)
	}
}

func TestRecoveryAtEveryPublicationBoundary(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"lock", "intent", "file", "index", "ref", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			_, db, _, _ := setup(t)
			root := t.TempDir()
			os.Chmod(root, 0700)
			w, err := OpenRecords(root, db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			stopped := errors.New("publication interrupted")
			w.afterPublication = func(point string) error {
				if point == stage {
					return stopped
				}
				return nil
			}
			s, err := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
			if err != nil {
				t.Fatal(err)
			}
			c := present(t, s)
			req := result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}
			receipt, err := s.Save(t.Context(), scope, req)
			if !errors.Is(err, stopped) || receipt.State != "unknown" {
				t.Fatalf("stage %s: %+v %v", stage, receipt, err)
			}
			w.Close()
			w, err = OpenRecords(root, db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if _, err := os.Stat(filepath.Join(root, "versions/records/.git/index.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stale lock", err)
			}
			s, err = New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err = s.Save(t.Context(), scope, req)
			if err != nil || receipt.State != "unknown" {
				t.Fatal("recovery redispatched original", receipt, err)
			}
			c = present(t, s)
			receipt, err = s.Save(t.Context(), scope, result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}})
			if err != nil || receipt.State != "applied" {
				t.Fatal(receipt, err)
			}
			files, err := os.ReadDir(filepath.Join(root, "versions/records/resources"))
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if stage == "lock" {
				want = 1
			}
			if len(files) != want {
				t.Fatalf("records=%d want %d", len(files), want)
			}
		})
	}
}
func TestRecoveryPreservesOutsideEditAndForeignLock(t *testing.T) {
	t.Parallel()
	for _, external := range []string{"file", "lock"} {
		t.Run(external, func(t *testing.T) {
			t.Parallel()
			_, db, _, _ := setup(t)
			root := t.TempDir()
			os.Chmod(root, 0700)
			w, err := OpenRecords(root, db, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			w.afterPublication = func(stage string) error {
				if stage == "file" {
					return errors.New("interrupted")
				}
				return nil
			}
			s, _ := New(db, Config{Journal: LedgerJournal{Surface: ledger.Surface{Kind: "tui"}}, Writer: w})
			c := present(t, s)
			if _, err := s.Save(t.Context(), scope, result.SaveRequest{ID: c.ID, Revision: 1, Destination: result.Destination{Kind: "resource"}}); err == nil {
				t.Fatal("fault not injected")
			}
			w.Close()
			name := "resources/notes.md"
			if external == "lock" {
				name = ".git/index.lock"
			}
			target := filepath.Join(root, "versions/records", name)
			if err := os.WriteFile(target, []byte("outside content"), 0600); err != nil {
				t.Fatal(err)
			}
			if recovered, err := OpenRecords(root, db, nil, nil); !errors.Is(err, ErrRecordsChanged) {
				if recovered != nil {
					recovered.Close()
				}
				t.Fatalf("external state accepted: %v", err)
			}
			bytes, err := os.ReadFile(target)
			if err != nil || string(bytes) != "outside content" {
				t.Fatal("outside state changed", string(bytes), err)
			}
		})
	}
}
