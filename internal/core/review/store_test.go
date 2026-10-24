// SPDX-License-Identifier: Apache-2.0
package review

import (
	"context"
	"database/sql"
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

func finishReview(t *testing.T, db *canon.Store, r Record) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return FinishIn(t.Context(), tx, r) }); err != nil {
		t.Fatal(err)
	}
}

func head(c byte) string { return strings.Repeat(string(c), 40) }

func TestOneClaimPerDeliveredHeadSurvivesServiceRecreation(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	r := Record{Key: Key{Repository: "Owner/Repo", Number: 7, Head: head('a')}, Task: "task", Receipt: "receipt", Generation: 1}
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
}

func TestSecondReviewRequestForTheSamePullRequestIsRefused(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	first := Record{Key: Key{Repository: "owner/repo", Number: 7, Head: head('a')}, Task: "task", Receipt: "receipt", Generation: 1}
	if err := s.Claim(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	// A new head or a new generation while the review is claimed is a second
	// review request.
	for _, r := range []Record{
		{Key: Key{Repository: "owner/repo", Number: 7, Head: head('b')}, Task: "task", Receipt: "receipt-2", Generation: 2},
		{Key: first.Key, Task: "task", Receipt: "receipt-2", Generation: 2},
	} {
		if err := s.Claim(t.Context(), r); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("second request accepted: %+v %v", r, err)
		}
	}
	done := first
	done.State, done.Verdict, done.Evidence = "complete", "needs_changes", "The write area was missed."
	done.Findings = []Finding{{ID: "core-scope-1", Severity: "P1", File: "outside.txt", Claim: "Changed path is outside the declared write area."}}
	finishReview(t, db, done)

	corrected := Record{Key: Key{Repository: "owner/repo", Number: 7, Head: head('b')}, Task: "task", Receipt: "receipt-2", Generation: 2}
	// Without the single return's evidence a new head is a second review.
	if err := s.Claim(t.Context(), corrected); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("head without a return was reviewed: %v", err)
	}
	// Another task cannot continue this pull request's review.
	other := corrected
	other.Task, other.ReturnOf = "other", "receipt"
	if err := s.Claim(t.Context(), other); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("another task continued the review: %v", err)
	}
	// The one check of the head produced by the return is accepted.
	corrected.ReturnOf = "receipt"
	if err := s.Claim(t.Context(), corrected); err != nil {
		t.Fatalf("corrected head was not checked: %v", err)
	}
	// A third request is refused even with return evidence.
	third := Record{Key: Key{Repository: "owner/repo", Number: 7, Head: head('c')}, Task: "task", Receipt: "receipt-3", Generation: 3, ReturnOf: "receipt-2"}
	if err := s.Claim(t.Context(), third); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("third review request accepted: %v", err)
	}
	got, err := s.Get(t.Context(), corrected.Key)
	if err != nil || got.State != "checking" || got.ReturnOf != "receipt" || got.Generation != 2 {
		t.Fatalf("refused request changed the continuation: %+v %v", got, err)
	}
	if _, err = s.Get(t.Context(), third.Key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("refused request was stored: %v", err)
	}
}

func TestVerifiedPullRequestIsNotReviewedAgain(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	first := Record{Key: Key{Repository: "owner/repo", Number: 8, Head: head('a')}, Task: "task", Receipt: "receipt", Generation: 1}
	if err := s.Claim(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	first.State, first.Verdict, first.Evidence = "complete", "verified", "The result meets the definition."
	finishReview(t, db, first)
	next := Record{Key: Key{Repository: "owner/repo", Number: 8, Head: head('b')}, Task: "task", Receipt: "receipt-2", Generation: 2, ReturnOf: "receipt"}
	if err := s.Claim(t.Context(), next); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("verified delivery reviewed again: %v", err)
	}
}

func TestBlockedReviewKeepsCoreCheckFindings(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	r := Record{Key: Key{Repository: "owner/repo", Number: 9, Head: head('a')}, Task: "task", Receipt: "receipt", Generation: 1}
	if err := s.Claim(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	r.State, r.Cause, r.Evidence = "blocked", "merge_precondition", "Required check failed: foundation"
	r.Findings = []Finding{{ID: "core-check-1", Severity: "P1", File: CheckFile, Claim: "Required check failed: foundation"}}
	finishReview(t, db, r)
	got, err := s.Get(t.Context(), r.Key)
	if err != nil || got.State != "blocked" || got.Verdict != "" || len(got.Findings) != 1 || got.Findings[0].Severity != "P1" {
		t.Fatalf("blocked check finding lost: %+v %v", got, err)
	}
}

func TestResultCannotBeSubstitutedOrOverwritten(t *testing.T) {
	db := reviewDB(t)
	s := New(db)
	r := Record{Key: Key{Repository: "owner/repo", Number: 7, Head: head('a')}, Task: "task", Receipt: "receipt", Generation: 1}
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
