// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/tools"
)

type inputRecoveryProbe struct {
	*transferProbe
	beforeInput func()
}

func (p *inputRecoveryProbe) Input(ctx context.Context, th worker.Thread, in worker.Input) error {
	p.beforeInput()
	return p.transferProbe.Input(ctx, th, in)
}

func TestWorkerInputRecoveryAdmission(t *testing.T) {
	for _, mode := range []string{"idle", "active_to_idle", "steer", "refused", "uncertain", "intent_failure", "checkpoint_failure"} {
		t.Run(mode, func(t *testing.T) {
			r, entry, base := reviewWorkerFixture(t)
			r.agent, r.conversation = "agent", "main:agent"
			r.activities = lifecycle.NewRegistry(nil)
			base.snapshot.State = worker.Idle
			prior := recovery.Record{ID: entry.thread.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: "input", Workdir: entry.workdir, Thread: entry.thread, State: "finished", Retries: 1, Finish: &worker.Finish{Status: worker.Done}}
			if mode == "active_to_idle" || mode == "steer" {
				base.snapshot.State = worker.Active
				prior.State, prior.Finish, prior.Reservation = "running", nil, entry.reservation
			}
			if mode == "steer" {
				base.next = "old"
			}
			if mode == "refused" {
				base.inputErr = worker.ErrInputRefused
			}
			if mode == "uncertain" {
				base.inputErr = context.DeadlineExceeded
			}
			store := recovery.Recovery{DB: r.db}
			if err := store.Put(t.Context(), prior); err != nil {
				t.Fatal(err)
			}
			entry.recovery = prior
			read := func() recovery.Record {
				t.Helper()
				records, err := store.Records(t.Context())
				if err != nil || len(records) != 1 {
					t.Fatalf("recovery records: %+v %v", records, err)
				}
				return records[0]
			}
			if mode == "intent_failure" || mode == "checkpoint_failure" {
				state := "unknown"
				if mode == "checkpoint_failure" {
					state = "running"
				}
				err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_followup_checkpoint BEFORE UPDATE ON worker_recovery WHEN json_extract(NEW.record,'$.State')='`+state+`' BEGIN SELECT RAISE(FAIL,'fixture checkpoint failure'); END`)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			entry.adapter = &inputRecoveryProbe{transferProbe: base, beforeInput: func() {
				rec := read()
				if rec.State != "unknown" || rec.Thread.TurnID != "" || rec.Finish != nil || rec.Reservation == "" || rec.Reservation != entry.reservation || rec.Retries != prior.Retries {
					t.Errorf("input reached worker without durable uncertain intent: %+v reservation=%q", rec, entry.reservation)
				}
			}}
			raw, _ := json.Marshal(workerArgs{Session: entry.thread.ID, Turn: entry.thread.TurnID, Generation: 1, Binding: 1, Text: "Continue the work"})
			result, err := r.workerOperation(t.Context(), "worker_input", "conversation-turn", ledger.Event{}, tools.Input{Arguments: raw})
			rec := read()
			switch mode {
			case "intent_failure":
				if base.inputs != 0 || result.State != tools.Failed || rec.State != prior.State || entry.reservation != "" {
					t.Fatalf("failed intent admitted work: inputs=%d result=%+v rec=%+v reservation=%q err=%v", base.inputs, result, rec, entry.reservation, err)
				}
			case "refused":
				if err != nil || result.State != tools.Failed || rec.State != prior.State || rec.Thread != prior.Thread || rec.Reservation != "" || entry.reservation != "" {
					t.Fatalf("refusal did not restore prior binding: result=%+v rec=%+v reservation=%q err=%v", result, rec, entry.reservation, err)
				}
			case "uncertain", "checkpoint_failure":
				if err == nil || result.State != tools.Unknown || rec.State != "unknown" || rec.Thread.TurnID != "" || rec.Reservation != entry.reservation || entry.reservation == "" {
					t.Fatalf("uncertain input lost durable fence: result=%+v rec=%+v reservation=%q err=%v", result, rec, entry.reservation, err)
				}
				if mode == "uncertain" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				// A fresh read of the previous completed turn cannot settle the intent.
				old := worker.ThreadSnapshot{Thread: prior.Thread, State: worker.Idle, Finish: &worker.Finish{Status: worker.Done}, EffectsKnown: true}
				if next, send := recovery.Observe(rec, old); next.State != "unknown" || send {
					t.Fatal("previous turn settled pending input", next, send)
				}
			default:
				if err != nil || result.State != tools.Applied || rec.State != "running" || rec.Thread != base.snapshot.Thread || rec.Finish != nil || rec.Reservation != entry.reservation || rec.Retries != prior.Retries {
					t.Fatalf("accepted input lacks notification-independent checkpoint: result=%+v rec=%+v reservation=%q err=%v", result, rec, entry.reservation, err)
				}
			}
			if mode != "refused" && mode != "intent_failure" {
				if err := r.work.CheckWorker(t.Context(), entry.reservation); err != nil {
					t.Fatal("input lost directory reservation", err)
				}
			}
			for _, activity := range entry.active {
				activity.Release()
			}
		})
	}
}
