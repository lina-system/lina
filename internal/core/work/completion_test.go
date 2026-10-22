// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestCompletionCommittedWithFinish(t *testing.T) {
	s, db := testStore(t)
	startFixture(t, s)
	if err := s.Submit(t.Context(), "run-1", receiptFixture()); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_completions").Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("receipt reacted before observed termination")
	}
	for i := 0; i < 2; i++ {
		if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatal("completion did not collapse to one durable row")
	}
}

func TestCompletionInsertFailureRollsBackFinish(t *testing.T) {
	s, db := testStore(t)
	startFixture(t, s)
	if err := s.Submit(t.Context(), "run-1", receiptFixture()); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_completion BEFORE INSERT ON work_completions BEGIN SELECT RAISE(ABORT,'injected completion failure'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err == nil {
		t.Fatal("injected failure was ignored")
	}
	card, err := s.Get(context.Background(), "task-1")
	if err != nil || !card.Active || card.TurnStatus != "" {
		t.Fatalf("partial state committed: %+v %v", card, err)
	}
}

func TestCompletionClaimsAndReconcile(t *testing.T) {
	s, db := testStore(t)
	startFixture(t, s)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Submit(ctx, "run-1", receiptFixture()))
	head := strings.Repeat("a", 40)
	must(db.Write(ctx, func(tx *canon.Tx) error { return FinishIn(ctx, tx, "task-1", "run-1", "completed", head) }))
	pending, err := s.PendingCompletions(ctx, "1")
	must(err)
	if len(pending) != 1 || pending[0].Head != head {
		t.Fatal(pending)
	}
	e := pending[0]
	// A simultaneous claim has exactly one owner, after committed state exists.
	var wg sync.WaitGroup
	claims := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, ok, err := s.ClaimCompletion(ctx, e.ID, "1")
			if err != nil {
				t.Error(err)
			}
			if ok && (c.Active || c.Receipt == nil) {
				t.Error("reacted to unfinished state")
			}
			claims <- ok
		}()
	}
	wg.Wait()
	close(claims)
	n := 0
	for ok := range claims {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("claims=%d", n)
	}
	// The next epoch resumes an unacknowledged claim, using the same identity.
	resumed, _, ok, err := s.ClaimCompletion(ctx, e.ID, "2")
	must(err)
	if !ok || resumed != e {
		t.Fatal("lost restart claim")
	}
	must(s.AcknowledgeCompletion(ctx, e.ID, "2"))
	must(db.Write(ctx, func(tx *canon.Tx) error { _, err := tx.ExecContext(ctx, "DELETE FROM work_completions"); return err }))
	for i := 0; i < 2; i++ {
		must(s.ReconcileCompletions(ctx))
	}
	var count int
	must(db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM work_completions").Scan(&count)
	}))
	pending, err = s.PendingCompletions(ctx, "3")
	must(err)
	if count != 1 || len(pending) != 0 {
		t.Fatalf("lost event recreated or reacted twice: rows=%d pending=%v", count, pending)
	}
}

func TestCompletionIgnoresOldHeadAndAttempt(t *testing.T) {
	for _, change := range []string{"head", "attempt"} {
		t.Run(change, func(t *testing.T) {
			s, db := testStore(t)
			startFixture(t, s)
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(s.Submit(ctx, "run-1", receiptFixture()))
			must(db.Write(ctx, func(tx *canon.Tx) error {
				return FinishIn(ctx, tx, "task-1", "run-1", "completed", strings.Repeat("a", 40))
			}))
			pending, err := s.PendingCompletions(ctx, "1")
			must(err)
			if len(pending) != 1 {
				t.Fatal(pending)
			}
			if change == "head" {
				must(s.ObserveHead(ctx, "task-1", "run-1", strings.Repeat("b", 40)))
			} else {
				r := runFixture()
				r.ID = "run-2"
				r.Generation = 2
				must(s.Start(ctx, r))
			}
			_, _, claimed, err := s.ClaimCompletion(ctx, pending[0].ID, "1")
			must(err)
			if claimed {
				t.Fatal("obsolete event claimed")
			}
			must(s.ReconcileCompletions(ctx))
			pending, err = s.PendingCompletions(ctx, "2")
			must(err)
			if len(pending) != 0 {
				t.Fatal("sweep replayed obsolete completion", pending)
			}
		})
	}
}

func TestCompletionReceiptAfterTermination(t *testing.T) {
	s, _ := testStore(t)
	startFixture(t, s)
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingCompletions(t.Context(), "1")
	if err != nil || len(pending) != 0 {
		t.Fatal("invented receipt", pending, err)
	}
	if err = s.Submit(t.Context(), "run-1", receiptFixture()); err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingCompletions(t.Context(), "1")
	if err != nil || len(pending) != 1 {
		t.Fatal("late receipt lost", pending, err)
	}
}
