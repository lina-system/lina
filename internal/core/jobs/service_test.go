// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

func fixture(t *testing.T) (*Service, *canon.Store, *int) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
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
	store, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.db"), Epoch: 1, Controls: controls, Migrations: []canon.Migration{{Version: 1, SQL: Schema + ";" + budget.Schema + `; CREATE TABLE accepted (sequence INTEGER PRIMARY KEY, body BLOB);`}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	calls := new(int)
	service, err := New(Config{Store: store, Epoch: 1, Limit: 100, Definitions: map[string]Definition{"memory": {
		Agent: "agent", Account: "account", Provider: provider.ChatGPT,
		Model: func(ctx context.Context, c Call) (Result, error) {
			*calls++
			if c.Kind != provider.Auxiliary || c.MaxTokens != 10 {
				t.Errorf("call: %+v", c)
			}
			return Result{Output: json.RawMessage(`{"text":"remembered"}`)}, nil
		},
		Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO accepted VALUES (?,?)", task.Sequence, []byte(output))
			return err
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(t.Context(), jobapi.Job{ID: "memory", Kind: jobapi.Memory, Enabled: true, Every: time.Minute, Timeout: time.Second, Budget: 100, MaxTokens: 10, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	if err = service.Enqueue(t.Context(), "memory", jobapi.Task{Sequence: 1, Input: json.RawMessage(`{"source":"synthetic"}`)}); err != nil {
		t.Fatal(err)
	}
	return service, store, calls
}

func TestApplyAtomicIdempotentAndFenced(t *testing.T) {
	s, store, calls := fixture(t)
	req := jobapi.AuxRequest{Epoch: 1, JobID: "memory", From: 0, Through: 1}
	aux, err := s.Aux(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Aux(t.Context(), req)
	if err != nil || string(aux.Output) != string(again.Output) || *calls != 1 {
		t.Fatalf("Aux replay: %v calls=%d", err, *calls)
	}
	p := jobapi.Proposal{Epoch: 1, JobID: "memory", From: 0, Through: 1, Output: aux.Output}
	first, err := s.Apply(t.Context(), p)
	if err != nil || first.Watermark != 1 {
		t.Fatalf("apply: %+v %v", first, err)
	}
	second, err := s.Apply(t.Context(), p)
	if err != nil || second != first {
		t.Fatalf("replay: %+v %v", second, err)
	}
	p.Output = json.RawMessage(`{"text":"changed"}`)
	if _, err = s.Apply(t.Context(), p); !errors.Is(err, jobapi.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	p.Epoch = 2
	if _, err = s.Apply(t.Context(), p); !errors.Is(err, jobapi.ErrEpoch) {
		t.Fatalf("epoch: %v", err)
	}
	var count int
	if err = store.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM accepted").Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("writes=%d %v", count, err)
	}
}

func TestApplyRollbackAndControls(t *testing.T) {
	s, store, _ := fixture(t)
	reject := errors.New("write rule rejected")
	d := s.cfg.Definitions["memory"]
	d.Apply = func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		tx.ExecContext(ctx, "INSERT INTO accepted VALUES (1,?)", []byte(output))
		return reject
	}
	s.cfg.Definitions["memory"] = d
	aux, err := s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: aux.Output}); !errors.Is(err, reject) {
		t.Fatal(err)
	}
	if err = store.Read(t.Context(), func(tx *canon.Tx) error {
		j, err := readJob(t.Context(), tx, "memory")
		if j.Watermark != 0 {
			t.Fatal("watermark advanced")
		}
		var n int
		tx.QueryRowContext(t.Context(), "SELECT count(*) FROM accepted").Scan(&n)
		if n != 0 {
			t.Fatal("write survived rollback")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(t.Context(), "memory", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: aux.Output}); !errors.Is(err, jobapi.ErrDisabled) {
		t.Fatal(err)
	}
	if _, err = s.Aux(t.Context(), jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1}); !errors.Is(err, jobapi.ErrDisabled) {
		t.Fatal(err)
	}
}

func TestBudgetScheduleAndDurableAuxReplay(t *testing.T) {
	for _, limit := range []string{"global", "job"} {
		t.Run(limit, func(t *testing.T) {
			s, store, calls := fixture(t)
			if limit == "global" {
				s.cfg.Limit = 10
			} else {
				if err := s.apply(t.Context(), 1, func(tx *canon.Tx) error {
					j, err := readJob(t.Context(), tx, "memory")
					if err != nil {
						return err
					}
					j.Budget = 10
					return saveJob(t.Context(), tx, j)
				}); err != nil {
					t.Fatal(err)
				}
			}
			r := jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1}
			aux, err := s.Aux(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := New(s.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = restarted.Aux(t.Context(), r); err != nil || *calls != 1 {
				t.Fatalf("replay: %v calls=%d", err, *calls)
			}
			if _, err = s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: aux.Output}); err != nil {
				t.Fatal(err)
			}
			if err = s.Enqueue(t.Context(), "memory", jobapi.Task{Sequence: 2, Input: json.RawMessage(`{"next":true}`)}); err != nil {
				t.Fatal(err)
			}
			r.From = 1
			r.Through = 2
			if _, err = s.Aux(t.Context(), r); !errors.Is(err, jobapi.ErrNotDue) {
				t.Fatalf("schedule: %v", err)
			}
			s.cfg.Now = func() time.Time { return time.Now().Add(time.Hour) }
			if _, err = s.Aux(t.Context(), r); !errors.Is(err, jobapi.ErrBudget) {
				t.Fatalf("budget: %v", err)
			}
			if *calls != 1 {
				t.Fatal("exceeded budget")
			}
			var charged int64
			if err = store.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT SUM(charged) FROM budget_usage").Scan(&charged)
			}); err != nil || charged != 10 {
				t.Fatalf("unknown usage charge=%d %v", charged, err)
			}
		})
	}
}

func TestUnknownAuxOutcomeIsNotAutomaticallyRepeated(t *testing.T) {
	s, _, _ := fixture(t)
	calls := 0
	d := s.cfg.Definitions["memory"]
	d.Model = func(context.Context, Call) (Result, error) { calls++; return Result{}, errors.New("provider failure") }
	s.cfg.Definitions["memory"] = d
	req := jobapi.AuxRequest{Epoch: 1, JobID: "memory", Through: 1}
	if _, err := s.Aux(t.Context(), req); !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatal(err)
	}
	s, err := New(s.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Aux(t.Context(), req); !errors.Is(err, jobapi.ErrPending) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("retried an unknown provider outcome")
	}
}

func TestReportCannotAdvanceProgressOrSupplyPolicy(t *testing.T) {
	s, _, _ := fixture(t)
	for _, state := range []jobapi.JobState{{Epoch: 2, Status: "failed"}, {Epoch: 1, Watermark: 1, Status: "applied"}, {Epoch: 1, Status: "enable"}} {
		if err := s.Report(t.Context(), "memory", state); err == nil {
			t.Fatalf("accepted %+v", state)
		}
	}
	if err := s.Report(t.Context(), "memory", jobapi.JobState{Epoch: 1, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	// Deterministic jobs may propose a write without spending any model budget.
	if _, err := s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: json.RawMessage(`{"imported":true}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonFenceRejectsCurrentRequestAfterEpochAdvance(t *testing.T) {
	s, store, _ := fixture(t)
	var path string
	if err := store.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path)
	}); err != nil {
		t.Fatal(err)
	}
	newer, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	if _, err = newer.Exec("UPDATE canon_meta SET epoch='2'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "memory", Through: 1, Output: json.RawMessage(`{"memory":"old"}`)}); !errors.Is(err, jobapi.ErrEpoch) {
		t.Fatalf("fence: %v", err)
	}
	if err = store.Read(t.Context(), func(tx *canon.Tx) error {
		j, err := readJob(t.Context(), tx, "memory")
		if j.Watermark != 0 {
			t.Fatal("stale writer advanced progress")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
