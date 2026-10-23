// SPDX-License-Identifier: Apache-2.0
package reactor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
)

func fixture(t *testing.T) (*Reactor, func()) {
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
	w := work.New(db)
	if err = w.Create(t.Context(), work.Task{ID: "task", AgentID: "agent", ConversationID: "conversation", Title: "Result", Done: "result received"}); err != nil {
		t.Fatal(err)
	}
	if err = w.Start(t.Context(), work.Run{ID: "attempt", TaskID: "task", Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: "task", ExecutionGeneration: 1, Attempt: 1, RevisionHash: strings.Repeat("0", 64), Outcome: "failed", Producer: "child", TurnRef: work.TurnRef{ThreadID: "thread", TurnID: "turn", TurnStatus: "failed"}, EmittedAt: time.Now()}
	receipt.EventID = work.ReceiptID(receipt)
	if err = w.Submit(t.Context(), "attempt", receipt); err != nil {
		t.Fatal(err)
	}
	r := &Reactor{DB: db, Work: w, Epoch: "1"}
	finish := func() {
		t.Helper()
		if err := w.Finish(t.Context(), "task", "attempt", "failed"); err != nil {
			t.Fatal(err)
		}
	}
	return r, finish
}

func TestCommittedWakeupAndLostSignal(t *testing.T) {
	r, finish := fixture(t)
	observed := make(chan work.Completion, 2)
	r.On = func(ctx context.Context, e work.Completion, c work.Card) error {
		var count int
		err := r.DB.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM work_completions WHERE id=?", e.ID).Scan(&count)
		})
		if err != nil || count != 1 || c.Active || c.Receipt == nil {
			t.Errorf("reaction preceded durable state: %d %+v %v", count, c, err)
		}
		observed <- e
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	finish()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("commit did not wake reactor")
	}
	// Observe the committed acknowledgement before cancellation/fault injection.
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		changed := r.DB.Changed()
		var n int
		if err := r.DB.Read(wait, func(tx *canon.Tx) error {
			return tx.QueryRowContext(wait, "SELECT count(*) FROM work_completion_claims WHERE state='handled'").Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		select {
		case <-changed:
		case <-wait.Done():
			t.Fatal(wait.Err())
		}
	}
	cancel()
	<-done
	if err := r.DB.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM work_completions")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.Sweep(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(observed) != 0 {
		t.Fatal("sweep invoked a reaction")
	}
	r.Epoch = "2"
	if err := r.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 0 {
		t.Fatal("lost handled signal replayed")
	}
}

func TestLostUnclaimedSignalAndRetry(t *testing.T) {
	r, finish := fixture(t)
	finish()
	ctx := t.Context()
	if err := r.DB.Write(ctx, func(tx *canon.Tx) error { _, err := tx.ExecContext(ctx, "DELETE FROM work_completions"); return err }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	failed := errors.New("consumer unavailable")
	r.On = func(context.Context, work.Completion, work.Card) error { calls++; return failed }
	for i := 0; i < 2; i++ {
		if err := r.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("sweep called consumer")
	}
	if err := r.Drain(ctx); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if err := r.Drain(ctx); err != nil || calls != 1 {
		t.Fatalf("busy retry: %d %v", calls, err)
	}
	if err := r.Work.RetryCompletions(ctx, r.Epoch); err != nil {
		t.Fatal(err)
	}
	r.On = func(context.Context, work.Completion, work.Card) error { calls++; return nil }
	if err := r.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	r.Epoch = "2"
	if err := r.Drain(ctx); err != nil || calls != 2 {
		t.Fatalf("handled event replayed: %d %v", calls, err)
	}
}
