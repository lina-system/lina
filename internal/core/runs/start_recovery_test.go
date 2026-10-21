// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func startCall(t *testing.T, b runs.Brief) provider.ToolCall {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return provider.ToolCall{ID: "start-1", Name: runs.StartToolName, Arguments: string(raw)}
}
func startReceipt(t *testing.T, s *runs.Service, o runs.Origin, c provider.ToolCall) runs.Receipt {
	t.Helper()
	result := s.StartTool(o).Execute(t.Context(), c)
	if result.Code != loop.OK {
		t.Fatalf("start: %+v", result)
	}
	var receipt runs.Receipt
	if err := json.Unmarshal(result.Output, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}
func TestStartToolReceiptSurvivesRetryAndRestart(t *testing.T) {
	var cfg runs.Config
	s, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("start invoked model before release")
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	}, func(c *runs.Config) { cfg = *c })
	call := startCall(t, brief())
	first := startReceipt(t, s, origin(), call)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got := startReceipt(t, s, origin(), call)
			if got != first {
				t.Errorf("duplicate receipt: %+v want %+v", got, first)
			}
		})
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	reopened, err := runs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := startReceipt(t, reopened, origin(), call); got != first {
		t.Fatalf("restart receipt: %+v want %+v", got, first)
	}
	var count int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM background_runs").Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("durable runs=%d err=%v", count, err)
	}
}
func TestStartToolRejectsConflictingBriefAndScopesCallIdentity(t *testing.T) {
	s, db, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("unexpected model call")
		return nil, nil
	})
	first := startReceipt(t, s, origin(), startCall(t, brief()))
	b := brief()
	b.Done = "different done condition"
	if got := s.StartTool(origin()).Execute(t.Context(), startCall(t, b)); got.Code != loop.Invalid {
		t.Fatalf("conflicting start accepted: %+v", got)
	}
	other := origin()
	other.Turn = "another-turn"
	if got := startReceipt(t, s, other, startCall(t, brief())); got.ID == first.ID {
		t.Fatal("distinct origin reused receipt")
	}
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM background_runs").Scan(&count)
	}); err != nil || count != 2 {
		t.Fatalf("durable runs=%d err=%v", count, err)
	}
}
func TestRecoverSchedulesReleasedQueueAndPreservesHolds(t *testing.T) {
	var cfg runs.Config
	s, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return answer(runs.Artifact{Body: "recovered", Done: true}), nil
	}, func(c *runs.Config) { cfg = *c })
	queued := start(t, s)
	unreleased := start(t, s)
	held := start(t, s)
	for _, id := range []string{queued, unreleased, held} {
		r, err := s.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		r.State = runs.Waiting
		r.Released = id != unreleased
		if id == held {
			r.Reason = "budget"
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "UPDATE background_runs SET record=? WHERE id=?", raw, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	resumed, err := runs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if err = resumed.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// Event-driven completion; the timeout only bounds a broken scheduler.
	if err = resumed.Wait(ctx, queued); err != nil {
		t.Fatalf("released waiting run did not resume: %v", err)
	}
	r, err := resumed.Get(t.Context(), queued)
	if err != nil || r.Outcome != runs.Complete {
		t.Fatalf("queued: %+v %v", r, err)
	}
	for _, id := range []string{unreleased, held} {
		r, err := resumed.Get(t.Context(), id)
		if err != nil || r.State != runs.Waiting {
			t.Fatalf("held/unreleased run executed: %+v %v", r, err)
		}
	}
}
