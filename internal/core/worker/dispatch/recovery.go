// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
)

// recoveryRecord derives a new attempt, never a replay of an earlier command.
// The caller commits it atomically with the old attempt's terminal observation,
// before either the completion reactor or the scheduler can act on that event.
func recoveryRecord(r Record) Record {
	snap := r.Snapshot
	if r.Restored || snap == nil || snap.Finish == nil {
		return r
	}
	next, send := recovery.Observe(recovery.Record{State: "unknown", Thread: snap.Thread, Retries: r.Recoveries, PendingRequests: r.PendingRequests, Canceled: r.Canceled}, *snap)
	r.State = "unknown"
	if send {
		r.Recoveries = next.Retries
		r.RecoveryThread = r.Thread
		r.RecoveryWorker = r.WorkerName()
		r.Tried = nil
		r.Packet = ""
		r.State = "queued"
		r.Code = "recovering"
		r.Snapshot = nil
		r.Announced = false
	} else if next.State == "decision" {
		r.State = "decision"
		r.Code = snap.Finish.Code
		r.Announced = false
		if r.DecisionAt == 0 {
			r.DecisionAt = time.Now().UnixNano()
		}
	} else if next.State == "finished" || next.State == "parked" {
		r.State = "finished"
	}
	return r
}

// Only a committed complete snapshot can settle a cut headless attempt. An
// absent snapshot stays unknown; starting a process is not a read of the old one.
func (s *Service) recoverRecord(ctx context.Context, r Record) (Record, error) {
	next := recoveryRecord(r)
	if r.Restored || r.Snapshot == nil || r.Snapshot.Finish == nil || next.State == "unknown" {
		return r, nil
	}
	if r.Snapshot.Finish.Status == worker.Done {
		card, err := s.cfg.Work.Get(ctx, r.ID)
		if err != nil {
			return r, err
		}
		if card.Receipt == nil || card.Run == nil || card.Run.ID != fmt.Sprintf("%s:%d", r.ID, max(1, r.Attempt)) {
			return r, work.ErrState
		}
	}
	status := "failed"
	if r.Snapshot.Finish.Status == worker.Done {
		status = "completed"
	}
	head := ""
	if r.PR != nil {
		head = r.PR.Head
	}
	err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := work.FinishIn(ctx, tx, r.ID, fmt.Sprintf("%s:%d", r.ID, max(1, r.Attempt)), status, head); err != nil {
			return err
		}
		return saveRecord(ctx, tx, next)
	})
	return next, err
}

func terminalSnapshot(ctx context.Context, adapter worker.Adapter, thread worker.Thread) (worker.ThreadSnapshot, error) {
	if read, ok := adapter.(interface {
		Reconcile(context.Context, worker.Thread) (worker.ThreadSnapshot, error)
	}); ok {
		return read.Reconcile(ctx, thread)
	}
	return adapter.Snapshot(ctx, thread)
}

// Cancel is a trusted Core control, persisted before an interrupt is sent.
func (s *Service) Cancel(ctx context.Context, id string) error {
	return s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE worker_dispatch SET record=json_set(record,'$.canceled',json('true')) WHERE id=?`, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return work.ErrNotFound
		}
		return err
	})
}

type resumable interface {
	worker.Adapter
	Recover(context.Context, worker.Thread, string, []worker.Tool) (worker.Thread, error)
	Reconcile(context.Context, worker.Thread) (worker.ThreadSnapshot, error)
}

// A settled-attempt restarter must not be used to observe an unknown attempt.
// ACP can restore a session, but its protocol cannot read an exact prior turn.
type attemptRecoverer interface {
	worker.Adapter
	RecoverAttempt(context.Context, lease.Lease, worker.StartSpec, worker.Thread) (worker.Thread, error)
}

func (s *Service) observeThread(ctx context.Context, l lease.Lease, r *Record, dir string) (worker.ThreadSnapshot, error) {
	for _, f := range s.workersFor(*r) {
		if f.Name != r.WorkerName() {
			continue
		}
		adapter, close, err := f.Open(ctx, l)
		if err != nil {
			return worker.ThreadSnapshot{}, err
		}
		defer close(context.Background())
		reader, ok := adapter.(resumable)
		if !ok {
			return worker.ThreadSnapshot{}, work.ErrState
		}
		th, err := reader.Recover(ctx, *r.Thread, dir, nil)
		if err != nil {
			return worker.ThreadSnapshot{}, err
		}
		snap, err := reader.Reconcile(ctx, th)
		if err == nil {
			r.Thread = &th
			snap.EffectsKnown = snap.EffectsKnown && len(r.PendingRequests) == 0
		}
		return snap, err
	}
	return worker.ThreadSnapshot{}, work.ErrState
}

func (s *Service) startAttempt(ctx context.Context, l lease.Lease, adapter worker.Adapter, r Record, attemptID, dir string) (worker.Thread, error) {
	spec := worker.StartSpec{TaskID: r.ID, AttemptID: attemptID, Workdir: dir, TaskPacket: r.Packet}
	if reader, ok := adapter.(attemptRecoverer); ok && r.Recoveries == 1 && r.RecoveryThread != nil && r.RecoveryWorker == adapter.Name() && len(r.Tried) == 1 {
		return reader.RecoverAttempt(ctx, l, spec, *r.RecoveryThread)
	}
	if reader, ok := adapter.(resumable); ok && r.RecoveryThread != nil && r.RecoveryWorker == adapter.Name() && len(r.Tried) == 1 {
		old := *r.RecoveryThread
		old.AttemptID = attemptID
		th, err := reader.Recover(ctx, old, dir, nil)
		if err != nil {
			return worker.Thread{}, err
		}
		snap, err := reader.Reconcile(ctx, th)
		if err != nil {
			return worker.Thread{}, err
		}
		if snap.Finish == nil || !snap.EffectsKnown || len(snap.Pending) > 0 {
			return worker.Thread{}, work.ErrState
		}
		if err = adapter.Input(ctx, th, worker.Input{Text: r.Packet}); err != nil {
			return worker.Thread{}, err
		}
		current, err := adapter.Snapshot(ctx, th)
		return current.Thread, err
	}
	return adapter.Start(ctx, l, spec)
}
