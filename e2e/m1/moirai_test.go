// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

// Reuse #34's isolated state, credential-free processes, readiness and cleanup.
// The production Core job service is hosted with synthetic job/model handlers;
// the existing Core command proves its client protocol has no worker dependency.
func TestM1MoiraiCrashResume(t *testing.T) {
	data, err := os.ReadFile("moirai-worker.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		JobID     string            `json:"job_id"`
		Kind      jobapi.Kind       `json:"kind"`
		Inputs    []json.RawMessage `json:"inputs"`
		Kill      int64             `json:"kill_during_sequence"`
		Watermark int64             `json:"watermark_after_restart"`
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	workerBinary := buildCommand(t, "moirai-worker")
	h := newHarness(t, binary)
	core := h.startCore()
	before := h.client(core)
	h.inputRefused(before)
	// Keep the job service's canon isolated from the connection-only Core command.
	state := filepath.Join(h.root, "job-state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	path := filepath.Join(state, "canon.db")
	cfg := canon.Config{Path: path, Epoch: 1, Controls: controls, Migrations: []canon.Migration{{Version: 1, SQL: jobs.Schema + ";" + budget.Schema + `;CREATE TABLE adopted(sequence INTEGER PRIMARY KEY,body BLOB);`}}}
	store, err := canon.Open(h.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	var logicalTime atomic.Int64
	logicalTime.Store(time.Now().Add(-time.Hour).UnixNano())
	service, err := jobs.New(jobs.Config{Store: store, Epoch: 1, Limit: 100, Now: func() time.Time { return time.Unix(0, logicalTime.Load()) }, Definitions: map[string]jobs.Definition{scenario.JobID: {
		Agent: "agent-test", Account: "account-test", Provider: provider.ChatGPT,
		Model: func(ctx context.Context, c jobs.Call) (jobs.Result, error) {
			n := calls.Add(1)
			if c.Kind != provider.Auxiliary || c.MaxTokens != 10 {
				return jobs.Result{}, errors.New("wrong model route")
			}
			if n == scenario.Kill {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return jobs.Result{}, ctx.Err()
				}
			}
			return jobs.Result{Output: json.RawMessage(fmt.Sprintf(`{"memory":%d}`, n))}, nil
		},
		Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
			// Stand-in for a concrete memory job's write rules. Only a validated result
			// for the current source task is admitted; the worker has no SQL surface.
			var proposal struct{ Memory int64 }
			if json.Unmarshal(output, &proposal) != nil || proposal.Memory != task.Sequence {
				return errors.New("invalid memory proposal")
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO adopted VALUES (?,?)", task.Sequence, []byte(output))
			return err
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(h.ctx, jobapi.Job{ID: scenario.JobID, Kind: scenario.Kind, Enabled: true, Every: time.Minute, Timeout: operationTimeout, Budget: 100, MaxTokens: 10, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	for i, input := range scenario.Inputs {
		if err = service.Enqueue(h.ctx, scenario.JobID, jobapi.Task{Sequence: int64(i + 1), Input: input}); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(state, "run", "jobs.sock")
	stopService := startJobService(t, h.ctx, service, socket, controls)
	worker := func() *process {
		child := *h
		child.binary = workerBinary
		p := child.command("--canon", path, "--socket", socket, "--once")
		p.start(t)
		return p
	}
	first := worker()
	first.wait(t, 0)
	// The first commit is durable before the second process starts.
	if got := jobWatermark(t, store, scenario.JobID); got != scenario.Kill-1 {
		t.Fatalf("first watermark=%d", got)
	}
	logicalTime.Add(int64(time.Minute))
	interrupted := worker()
	select {
	case <-started:
	case <-interrupted.done:
		t.Fatalf("worker exited before mid-job boundary: %v %s", interrupted.err, &interrupted.stderr)
	case <-h.ctx.Done():
		t.Fatal("worker did not reach mid-job boundary")
	}
	snapshot := canonSnapshot(t, store)
	interrupted.killAndWait()
	if got := canonSnapshot(t, store); got != snapshot {
		t.Fatal("killing the worker changed canon")
	}
	if got := jobWatermark(t, store, scenario.JobID); got != scenario.Kill-1 {
		t.Fatal("interrupted work advanced watermark")
	}
	// Core remains available while the worker is absent and the job is in flight.
	after := h.client(core)
	h.inputRefused(after)
	if before.welcome.Epoch != after.welcome.Epoch {
		t.Fatal("worker death restarted Core")
	}
	close(release)
	// Wait for durable Aux completion, not a delay chosen to make restart pass.
	client := jobapi.NewClient(socket)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		_, err = client.Aux(h.ctx, jobapi.AuxRequest{Epoch: 1, JobID: scenario.JobID, From: scenario.Kill - 1, Through: scenario.Kill})
		if err == nil {
			break
		}
		if !errors.Is(err, jobapi.ErrPending) {
			t.Fatal(err)
		}
		select {
		case <-ticker.C:
		case <-h.ctx.Done():
			t.Fatal("Aux result was not persisted")
		}
	}
	// Reopen canon and the Core service as well as the worker process.
	stopService()
	if err = store.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	store, err = canon.Open(h.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	service, err = jobs.New(jobs.Config{Store: store, Epoch: 1, Limit: 100, Now: func() time.Time { return time.Unix(0, logicalTime.Load()) }, Definitions: map[string]jobs.Definition{scenario.JobID: {
		Agent: "agent-test", Account: "account-test", Provider: provider.ChatGPT,
		Model: func(context.Context, jobs.Call) (jobs.Result, error) {
			calls.Add(1)
			return jobs.Result{}, errors.New("replayed model call")
		},
		Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO adopted VALUES (?,?)", task.Sequence, []byte(output))
			return err
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	stopService = startJobService(t, h.ctx, service, socket, controls)
	resumed := worker()
	resumed.wait(t, 0)
	if got := jobWatermark(t, store, scenario.JobID); got != scenario.Watermark {
		t.Fatalf("resumed watermark=%d", got)
	}
	if calls.Load() != int64(len(scenario.Inputs)) {
		t.Fatalf("model calls=%d", calls.Load())
	}
	var sequences []int64
	if err = store.Read(h.ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(h.ctx, "SELECT sequence FROM adopted ORDER BY sequence")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int64
			if err = rows.Scan(&n); err != nil {
				return err
			}
			sequences = append(sequences, n)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sequences, []int64{1, 2}) {
		t.Fatalf("adopted=%v", sequences)
	}
	idle := worker()
	idle.wait(t, 0)
	if calls.Load() != 2 {
		t.Fatal("completed job replayed")
	}
	// Without the job socket, pending background work stays untouched and the
	// independent Core client protocol keeps its identity and availability.
	stopService()
	if err = service.Enqueue(h.ctx, scenario.JobID, jobapi.Task{Sequence: 3, Input: json.RawMessage(`{"source":"pending"}`)}); err != nil {
		t.Fatal(err)
	}
	snapshot = canonSnapshot(t, store)
	absent := worker()
	absent.wait(t, 69)
	if canonSnapshot(t, store) != snapshot {
		t.Fatal("worker wrote canon without Core job API")
	}
	h.inputRefused(h.client(core))
	h.stopCore(core)
}

func jobWatermark(t *testing.T, store *canon.Store, id string) int64 {
	t.Helper()
	var data []byte
	var j jobapi.Job
	if err := store.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT record FROM moirai_jobs WHERE id=?", id).Scan(&data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatal(err)
	}
	return j.Watermark
}
func canonSnapshot(t *testing.T, store *canon.Store) string {
	t.Helper()
	var out strings.Builder
	if err := store.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"canon_meta", "moirai_jobs", "moirai_tasks", "moirai_aux", "moirai_applied", "moirai_reports", "budget_usage", "adopted"} {
			rows, err := tx.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1,2")
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					rows.Close()
					return err
				}
				data, _ := json.Marshal(values)
				out.WriteString(table)
				out.Write(data)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func startJobService(t *testing.T, ctx context.Context, service *jobs.Service, socket string, controls *ctlfiles.Store) func() {
	t.Helper()
	listener, err := jobs.Listen(socket, controls)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var result error
	go func() { defer close(done); result = service.Serve(ctx, listener) }()
	stop := func() {
		cancel()
		select {
		case <-done:
			if result != nil {
				t.Error(result)
			}
		case <-time.After(operationTimeout):
			t.Error("job service did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}
