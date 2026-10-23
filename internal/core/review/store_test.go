// SPDX-License-Identifier: Apache-2.0
package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func reviewDB(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func TestOneClaimPerDeliveredHeadSurvivesServiceRecreation(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	r := Record{Key: Key{Repository: "Owner/Repo", Number: 7, Head: strings.Repeat("a", 40)}, Task: "task", Receipt: "receipt", Generation: 1}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			err := s.Claim(t.Context(), r)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrDuplicate) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("claims=%d", accepted.Load())
	}
	r.Repository = "owner/repo"
	if err := New(db).Claim(t.Context(), r); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("recreated service allowed duplicate: %v", err)
	}
	r.Head = strings.Repeat("b", 40)
	r.Generation = 2
	r.Receipt = "receipt-2"
	if err := s.Claim(t.Context(), r); err != nil {
		t.Fatal("fix head rejected", err)
	}
}
func TestResultCannotBeSubstitutedOrOverwritten(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	r := Record{Key: Key{Repository: "owner/repo", Number: 7, Head: strings.Repeat("a", 40)}, Task: "task", Receipt: "receipt", Generation: 1}
	if err := s.Claim(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	r.State = "complete"
	r.Verdict = "needs_changes"
	r.Evidence = "The result misses the work definition."
	r.Findings = []Finding{{ID: "f1", Severity: "P1", File: "result.txt", Claim: "Missing result"}}
	altered := r
	altered.Receipt = "different"
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return FinishIn(t.Context(), tx, altered) }); err == nil {
		t.Fatal("receipt substituted")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return FinishIn(t.Context(), tx, r) }); err != nil {
		t.Fatal(err)
	}
	r.Verdict = "verified"
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return FinishIn(t.Context(), tx, r) }); err == nil {
		t.Fatal("verdict overwritten")
	}
	got, err := s.Get(t.Context(), r.Key)
	if err != nil || got.Verdict != "needs_changes" || len(got.Findings) != 1 {
		t.Fatalf("stored=%+v %v", got, err)
	}
}
