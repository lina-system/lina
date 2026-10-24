// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
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

func TestPreparationCallbackNeverReceivesRawToolSecrets(t *testing.T) {
	credential := "ghp_" + strings.Repeat("Synthetic", 5)
	var round atomic.Int64
	var prepared atomic.Int64
	s, _, _ := setupWith(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		raw, _ := json.Marshal(req.Items)
		if strings.Contains(string(raw), credential) {
			t.Error("credential reached model")
		}
		if round.Add(1) == 1 {
			return call("credential-read"), nil
		}
		return answer(runs.Artifact{Body: "masked report", Done: true}), nil
	}, func(c *runs.Config) {
		tool := c.Tools["read"]
		tool.Handler.Execute = func(_ context.Context, call provider.ToolCall) loop.Result {
			raw, _ := json.Marshal(map[string]string{"evidence": credential, "password": "synthetic-password"})
			return loop.Result{ID: call.ID, Code: loop.OK, Output: raw}
		}
		c.Tools["read"] = tool
		c.Prepare = func(_ context.Context, r runs.Record, items []provider.Item) ([]provider.Item, error) {
			prepared.Add(1)
			raw, _ := json.Marshal(struct {
				Record runs.Record
				Items  []provider.Item
			}{r, items})
			if strings.Contains(string(raw), credential) || strings.Contains(string(raw), "synthetic-password") {
				t.Error("raw tool secret reached preparation callback")
			}
			return items, nil
		}
	})
	id := start(t, s)
	release(t, s, id)
	r := wait(t, s, id)
	if r.Outcome != runs.Complete || prepared.Load() != 2 {
		t.Fatalf("outcome=%s preparations=%d", r.Outcome, prepared.Load())
	}
}

func TestCommittedStartReceiptSurvivesLaterBudgetHold(t *testing.T) {
	var held atomic.Bool
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("unexpected execution")
		return nil, nil
	}, func(c *runs.Config) {
		c.Authorize = func(context.Context, runs.Origin, runs.Brief) error {
			if held.Load() {
				return runs.ErrBudget
			}
			return nil
		}
	})
	c := startCall(t, brief())
	first := startReceipt(t, s, origin(), c)
	held.Store(true)
	got := s.StartTool(origin()).Execute(t.Context(), c)
	if got.Code != loop.OK {
		t.Fatalf("committed receipt blocked by later budget: %+v", got)
	}
	var receipt runs.Receipt
	if err := json.Unmarshal(got.Output, &receipt); err != nil || receipt != first {
		t.Fatalf("replay=%+v err=%v", receipt, err)
	}
	changed := brief()
	changed.Done = "changed"
	if got := s.StartTool(origin()).Execute(t.Context(), startCall(t, changed)); got.Code != loop.Invalid {
		t.Fatalf("conflicting replay: %+v", got)
	}
	c.ID = "new-call"
	if got := s.StartTool(origin()).Execute(t.Context(), c); got.Code != loop.Denied {
		t.Fatalf("new admission bypassed budget: %+v", got)
	}
}
