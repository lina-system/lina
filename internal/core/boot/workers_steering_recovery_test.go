// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

type steeringRecoveryProbe struct {
	workerProbe
	beforeInput       func()
	inputErr, readErr error
}

func (p *steeringRecoveryProbe) Input(ctx context.Context, th worker.Thread, in worker.Input) error {
	p.beforeInput()
	p.inputs++
	return p.inputErr
}

func (p *steeringRecoveryProbe) InputWithAdmission(ctx context.Context, th worker.Thread, in worker.Input, admit func(steering bool) error) error {
	if err := admit(true); err != nil {
		return errors.Join(worker.ErrInputRefused, err)
	}
	return p.Input(ctx, th, in)
}

func (p *steeringRecoveryProbe) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	if p.inputs > 0 && p.readErr != nil {
		return worker.ThreadSnapshot{}, p.readErr
	}
	return p.snapshot, nil
}

func (p *steeringRecoveryProbe) Reconcile(_ context.Context, th worker.Thread) (worker.ThreadSnapshot, error) {
	if th != p.snapshot.Thread {
		return worker.ThreadSnapshot{}, errors.New("lost exact reconciliation target")
	}
	return p.snapshot, nil
}

func TestWorkerSteeringRetainsRecoveryTarget(t *testing.T) {
	for _, mode := range []string{"snapshot_failure", "uncertain_send", "checkpoint_failure", "intent_failure", "refused"} {
		t.Run(mode, func(t *testing.T) {
			r := controlRuntime(t)
			now := time.Now()
			input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "steering-recovery", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce result"})
			if err != nil {
				t.Fatal(err)
			}
			th := worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
			dir := t.TempDir()
			if err := r.work.ReserveWorker(t.Context(), "steering", dir); err != nil {
				t.Fatal(err)
			}
			store := recovery.Recovery{DB: r.db}
			prior := recovery.Record{ID: th.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Workdir: dir, Thread: th, State: "running", Reservation: "steering", Retries: 1, RetryFrom: "previous-turn"}
			if err := store.Put(t.Context(), prior); err != nil {
				t.Fatal(err)
			}
			read := func() recovery.Record {
				t.Helper()
				records, err := store.Records(t.Context())
				if err != nil || len(records) != 1 {
					t.Fatalf("records=%+v err=%v", records, err)
				}
				return records[0]
			}
			probe := &steeringRecoveryProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Active}}}
			probe.beforeInput = func() {
				if rec := read(); rec.State != "unknown" || rec.Thread != th || rec.RetryFrom != prior.RetryFrom || rec.Reservation != prior.Reservation {
					t.Errorf("steering dispatched without exact durable target: %+v", rec)
				}
			}
			switch mode {
			case "snapshot_failure":
				probe.readErr = errors.New("transient snapshot failure")
			case "uncertain_send":
				probe.inputErr = context.DeadlineExceeded
			case "refused":
				probe.inputErr = worker.ErrInputRefused
			case "checkpoint_failure", "intent_failure":
				state := "running"
				if mode == "intent_failure" {
					state = "unknown"
				}
				err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_steering_checkpoint BEFORE UPDATE ON worker_recovery WHEN json_extract(NEW.record,'$.State')='`+state+`' BEGIN SELECT RAISE(FAIL,'checkpoint failure'); END`)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			released := false
			activity := lease.New(func() { released = true })
			defer activity.Release()
			e := &workerSession{origin: r, input: input, thread: th, adapter: probe, workdir: dir, reservation: prior.Reservation, reservationTurn: th.TurnID, recovery: prior, active: map[string]lease.Lease{th.TurnID: activity}}
			state, err := e.inputWorker(t.Context(), probe.snapshot, "Include failure details", lease.Lease{})
			want := tools.Unknown
			if mode == "refused" || mode == "intent_failure" {
				want = tools.Failed
			}
			if state != want || err == nil {
				t.Fatalf("state=%s err=%v", state, err)
			}
			rec := read()
			if rec.Thread != th || rec.RetryFrom != prior.RetryFrom || rec.Retries != prior.Retries || rec.Reservation != prior.Reservation || e.reservationTurn != th.TurnID || released {
				t.Fatalf("steering lost reconciliation target or execution ownership: rec=%+v turn=%q released=%v", rec, e.reservationTurn, released)
			}
			if mode != "refused" && mode != "intent_failure" {
				if state, _ := e.inputWorker(t.Context(), probe.snapshot, "Do not replay", lease.Lease{}); state != tools.Failed || probe.inputs != 1 {
					t.Fatal("uncertain steering admitted another input", state, probe.inputs)
				}
			}
			probe.snapshot.State, probe.snapshot.Finish, probe.snapshot.EffectsKnown = worker.Idle, &worker.Finish{Status: worker.Done}, true
			e.reconcileWorker(t.Context())
			wantInputs := 1
			if mode == "intent_failure" {
				wantInputs = 0
			}
			if rec := read(); rec.State != "finished" || rec.Reservation != "" || !released || len(e.active) != 0 || probe.inputs != wantInputs {
				t.Fatalf("exact terminal read did not settle steering: rec=%+v released=%v inputs=%d", rec, released, probe.inputs)
			}
			if err := r.work.CheckWorker(t.Context(), prior.Reservation); err == nil {
				t.Fatal("completed steering retained directory reservation")
			}
		})
	}
}
