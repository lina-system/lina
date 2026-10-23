// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// A queued start can predate the follow-up's returned binding. Serialize its
// read with input and only checkpoint the adapter's current exact turn.
func (e *workerSession) bindStartedRecovery(ctx context.Context, th worker.Thread) error {
	e.control.Lock()
	defer e.control.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	snap, err := e.adapter.Snapshot(readCtx, e.thread)
	if err != nil || snap.Thread != th {
		return err
	}
	e.transferRecoveryLease(th.TurnID)
	return e.bindRecovery(ctx, th)
}

func (e *workerSession) bindRecovery(ctx context.Context, th worker.Thread) error {
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	rec := e.recovery
	fresh := rec.ID == ""
	if fresh {
		packet, err := secrets.Mask(e.packet)
		if err != nil {
			return err
		}
		rec = recovery.Record{Reservation: e.reservation, Worker: "codex", ID: th.ID, Person: e.origin.person, Agent: e.origin.agent, Conversation: e.origin.conversation, Input: e.input.ID, Workdir: e.workdir, Packet: packet.Text(), State: "running", Epoch: e.origin.epoch}
	}
	priorTurn := rec.Thread.TurnID
	rec.Thread = th
	e.mu.Lock()
	rec.Reservation = e.reservation
	e.mu.Unlock()
	if th.TurnID != priorTurn || rec.RetryFrom != "" && th.TurnID != rec.RetryFrom {
		rec.State = "running"
		rec.Finish = nil
	}
	store := recovery.Recovery{DB: e.origin.db}
	var err error
	if fresh {
		err = store.Bind(ctx, rec)
	} else {
		err = store.Put(ctx, rec)
	}
	if err != nil {
		return err
	}
	e.recovery = rec
	return nil
}

func (r *conversationRuntime) startWorkerRecovery(ctx context.Context) error {
	store := recovery.Recovery{DB: r.db}
	records, err := store.Records(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if rec.State == "running" || rec.State == "retrying" {
			rec.State = "unknown"
			if err = store.Put(ctx, rec); err != nil {
				return err
			}
		}
	}
	b := r.workers
	b.recoveryWake = make(chan struct{}, 1)
	life, cancel := context.WithCancel(ctx)
	b.recoveryStop = cancel
	b.recoveryDone = make(chan struct{})
	go func() {
		defer close(b.recoveryDone)
		ticker := time.NewTicker(reactor.SweepInterval)
		defer ticker.Stop()
		attempted := map[string]bool{}
		for {
			r.reconcileWorkers(life, attempted)
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				attempted = map[string]bool{}
			case <-b.recoveryWake:
			}
		}
	}()
	return nil
}
func (b *workerRuntime) wakeRecovery() {
	if b.recoveryWake != nil {
		select {
		case b.recoveryWake <- struct{}{}:
		default:
		}
	}
}

func (r *conversationRuntime) reconcileWorkers(ctx context.Context, attempted map[string]bool) {
	records, err := (recovery.Recovery{DB: r.db}).Records(ctx)
	if err != nil {
		return
	}
	for _, rec := range records {
		if ctx.Err() != nil {
			return
		}
		if rec.State == "finished" || rec.State == "parked" {
			continue
		}
		r.workers.mu.Lock()
		e := r.workers.sessions[rec.ID]
		r.workers.mu.Unlock()
		if e != nil && rec.State != "decision" {
			select {
			case <-e.done:
				e = nil
			default:
			}
		}
		if e == nil {
			if attempted[rec.ID] {
				continue
			}
			attempted[rec.ID] = true
			if rec.Person != r.person {
				continue
			}
			input, err := r.journal().Get(ctx, rec.Input)
			if err != nil {
				continue
			}
			delivery, err := r.workerRecoveryView(ctx, input)
			if err != nil {
				continue
			}
			owner := newConversationView(r, rec.Agent, rec.Conversation)
			owner.codingDelivery = delivery
			e = &workerSession{reservation: rec.Reservation, reservationTurn: rec.Thread.TurnID, origin: owner, input: input, packet: rec.Packet, workdir: rec.Workdir, thread: rec.Thread, recovery: rec, done: make(chan struct{}), active: map[string]lease.Lease{}, approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}, started: time.Now()}
			retained := &retainedWorker{snapshot: worker.ThreadSnapshot{Thread: rec.Thread, State: worker.Unknown, Worker: rec.Worker, Workdir: rec.Workdir}}
			e.adapter = retained
			e.close = func() error { return nil }
			close(e.done)
			r.workers.mu.Lock()
			r.workers.sessions[rec.ID] = e
			r.workers.mu.Unlock()
			if rec.State == "decision" {
				e.recoveryDecision(rec)
				continue
			}
			if rec.Thread.TurnID == "" {
				continue
			} // no exact turn: never guess or launch again
			// Publish a new immutable entry only after its live binding is ready.
			e = &workerSession{reservation: rec.Reservation, reservationTurn: rec.Thread.TurnID, origin: owner, input: input, packet: rec.Packet, workdir: rec.Workdir, thread: rec.Thread, recovery: rec, done: make(chan struct{}), active: map[string]lease.Lease{}, approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}, started: time.Now()}
			process, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Reported, Reason: "worker reconciliation"})
			if err != nil {
				continue
			}
			adapter, err := codex.Open(ctx, process, codex.Config{Bind: e.bindRecovery})
			if err != nil {
				process.Release()
				continue
			}
			readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			th, err := adapter.Recover(readCtx, rec.Thread, rec.Workdir, workerWorkTools())
			cancel()
			if err != nil {
				adapter.Close()
				process.Release()
				continue
			}
			e.adapter, e.close, e.process, e.compatibility = adapter, adapter.Close, process, adapter.Compatibility()
			e.thread = th
			if err = e.bindRecovery(ctx, th); err != nil {
				adapter.Close()
				process.Release()
				continue
			}
			r.workers.mu.Lock()
			if r.workers.closed {
				r.workers.mu.Unlock()
				adapter.Close()
				process.Release()
				return
			}
			r.workers.sessions[rec.ID] = e
			r.workers.mu.Unlock()
			go e.observe()
		}
		e.reconcileWorker(ctx)
	}
}

func (e *workerSession) reconcileWorker(ctx context.Context) {
	e.control.Lock()
	defer e.control.Unlock()
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	rec := e.recovery
	if rec.ID == "" {
		return
	}
	if rec.State == "decision" {
		e.recoveryDecision(rec)
		return
	}
	if rec.State == "finished" || rec.State == "parked" {
		return
	}
	adapter, ok := e.adapter.(interface {
		Reconcile(context.Context, worker.Thread) (worker.ThreadSnapshot, error)
	})
	if !ok {
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	snap, err := adapter.Reconcile(readCtx, rec.Thread)
	cancel()
	if err != nil {
		return
	}
	next, send := recovery.Observe(rec, snap)
	next.ObservedAt = time.Now().UnixNano()
	if send || next.State == "finished" || next.State == "decision" || next.State == "parked" {
		// The exact terminal read replaces a missing FinishEvent for this turn.
		// A later turn's activity remains independently bound.
		e.mu.Lock()
		e.active[snap.Thread.TurnID].Release()
		delete(e.active, snap.Thread.TurnID)
		e.mu.Unlock()
	}
	if next.State == "decision" && next.DecisionAt == 0 {
		next.DecisionAt = time.Now().UnixNano()
	}
	if snap.Finish != nil && (next.State == "finished" || next.State == "decision") {
		if err = e.recoveredFinish(ctx, worker.Event{Kind: worker.FinishEvent, Thread: snap.Thread, Finish: snap.Finish}, snap.Output); err != nil {
			return
		}
	}

	// Only an exact, settled terminal observation can release the prior
	// directory fence. A new continuation must acquire admission again.
	if e.origin.work != nil && (send || next.State == "finished" || next.State == "decision" || next.State == "parked") {
		if rec.Reservation != "" {
			if err := e.origin.work.ReleaseWorker(ctx, rec.Reservation); err != nil {
				return
			}
		}
		next.Reservation = ""
		e.mu.Lock()
		e.reservation, e.reservationTurn = "", ""
		e.mu.Unlock()
	}
	reservationCreated, intentPersisted := false, false
	defer func() {
		if reservationCreated && !intentPersisted {
			_ = e.origin.work.ReleaseWorker(context.WithoutCancel(ctx), next.Reservation)
			e.mu.Lock()
			e.reservation, e.reservationTurn = "", ""
			e.mu.Unlock()
		}
	}()
	var activity lease.Lease
	if send {
		source := e.origin.workerDelivery()
		if source.validateChannel(ctx) != nil || source.channelBinding != nil && !source.channelBinding.Scope.Tools {
			return
		}
		arguments, _ := json.Marshal(workerArgs{Session: next.ID, Turn: next.Thread.TurnID, Generation: next.Thread.ProcessGeneration, Binding: next.Thread.BindingEpoch, Text: recoveryContinuation})
		// Recovery inherits the same preset and ownership; a stored role grants no
		// additional effect authority and an unavailable owner cannot execute.
		if _, err = e.origin.agents.Get(ctx, next.Agent); err != nil {
			return
		}
		if err = authority.CheckPreset(ctx, e.origin.db, e.origin.epoch, authority.PolicyRequest{Key: "worker-recovery:" + next.ID, Person: next.Person, Agent: next.Agent, TaskID: next.Thread.TaskID, Tool: "worker_input", Target: next.Workdir, Class: tools.WorkerEffect, Arguments: arguments}); err != nil {
			return
		}
		if e.origin.work != nil {
			next.Reservation = next.ID + ":recovery:" + fmt.Sprint(next.Retries)
			if err = e.origin.work.ReserveWorker(ctx, next.Reservation, next.Workdir); err != nil {
				return
			}
			reservationCreated = true
			e.mu.Lock()
			e.reservation = next.Reservation
			e.reservationTurn = ""
			e.mu.Unlock()
		}
		activity, err = e.origin.activities.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "worker recovery turn"})
		if err != nil {
			return
		}
	}
	if err = (recovery.Recovery{DB: e.origin.db}).Put(ctx, next); err != nil {
		activity.Release()
		return
	}
	intentPersisted = true
	e.recovery = next
	if next.State == "decision" {
		e.recoveryDecision(next)
	}
	if !send {
		return
	}
	e.mu.Lock()
	e.active["recovery:"+next.RetryFrom] = activity
	e.mu.Unlock()
	sendCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	err = e.adapter.Input(sendCtx, snap.Thread, worker.Input{Text: recoveryContinuation})
	stop()
	if err != nil {
		return
	}
	// The started event also checkpoints the new identity. Here we retain the
	// activity until its finish even when the notification precedes the reply.
	current, err := e.adapter.Snapshot(ctx, snap.Thread)
	if err != nil {
		return
	}
	next.Thread = current.Thread
	next.State = "running"
	if err = (recovery.Recovery{DB: e.origin.db}).Put(ctx, next); err != nil {
		return
	}
	e.recovery = next
	e.mu.Lock()
	e.reservationTurn = current.Thread.TurnID
	e.mu.Unlock()
	e.transferRecoveryLease(current.Thread.TurnID)
	e.origin.notifyWorkerRunning()
}

func (e *workerSession) recoveryDecision(rec recovery.Record) {
	id := "worker-request:recovery:" + rec.ID
	e.mu.Lock()
	if e.questions[id] != nil {
		e.mu.Unlock()
		return
	}
	recommendation := "Keep the work paused and inspect the result."
	card := protocol.Question{ConversationID: e.origin.workerDelivery().conversation, Card: &protocol.DecisionCard{ID: id, Revision: "1", Agent: e.origin.router.root.decisions.AgentName(e.origin.agent), Summary: "The worker failed again. The work is paused.", Recommendation: &recommendation, Targets: []protocol.DecisionTarget{}}, Form: &protocol.QuestionForm{PluginName: "Worker recovery", Fields: []protocol.FormField{{Name: "action", Type: protocol.FormFieldTypeChoice, Choices: &[]string{"Keep paused", "Stop"}}}}}
	e.questions[id] = &workerQuestion{card: card, kind: "recovery", requested: time.Unix(0, rec.DecisionAt)}
	e.mu.Unlock()
	_ = e.origin.router.root.decisions.PublishQuestion(e.origin.ctx, id)
	e.origin.notifyOwnerViews()
}
func (e *workerSession) answerRecovery(ctx context.Context, answer protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	if answer.Values == nil {
		return protocol.DecisionOutcomeRefused, nil
	}
	v := (*answer.Values)["action"].String
	if v == nil || (*v != "Keep paused" && *v != "Stop") {
		return protocol.DecisionOutcomeRefused, nil
	}
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	rec := e.recovery
	if rec.State != "decision" {
		return protocol.DecisionOutcomeRefused, errors.New("recovery decision unavailable")
	}
	rec.State = "parked"
	rec.Decision = *v
	rec.DecidedAt = time.Now().UnixNano()
	rec.Canceled = *v == "Stop"
	if err := (recovery.Recovery{DB: e.origin.db}).Put(ctx, rec); err != nil {
		return protocol.DecisionOutcomeRefused, err
	}
	e.recovery = rec
	return protocol.DecisionOutcomeResolved, nil
}

// Durable uncertainty and decisions stay visible when the worker cannot open.
// This adapter cannot execute; it is replaced only by a successful rebind.
type retainedWorker struct{ snapshot worker.ThreadSnapshot }

func (s *retainedWorker) Name() string                            { return s.snapshot.Worker }
func (s *retainedWorker) Version(context.Context) (string, error) { return s.snapshot.Version, nil }
func (*retainedWorker) Capabilities() worker.CapabilitySet        { return nil }
func (*retainedWorker) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, codex.ErrUnsupported
}
func (*retainedWorker) Input(context.Context, worker.Thread, worker.Input) error {
	return codex.ErrUnsupported
}
func (*retainedWorker) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	return codex.ErrUnsupported
}
func (*retainedWorker) Events() <-chan worker.Event { return nil }
func (*retainedWorker) Answer(context.Context, worker.Thread, worker.Answer) error {
	return codex.ErrUnsupported
}
func (*retainedWorker) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (s *retainedWorker) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return s.snapshot, nil
}

func (e *workerSession) trackRecoveryRequest(id string, pending bool) error {
	id = fmt.Sprintf("%d:%d:%s", e.thread.ProcessGeneration, e.thread.BindingEpoch, id)
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	rec := e.recovery
	if rec.ID == "" {
		return nil
	}
	requests := map[string]bool{}
	for key, value := range rec.PendingRequests {
		requests[key] = value
	}
	if pending {
		requests[id] = true
	} else {
		delete(requests, id)
	}
	rec.PendingRequests = requests
	if err := (recovery.Recovery{DB: e.origin.db}).Put(e.origin.ctx, rec); err != nil {
		return err
	}
	e.recovery = rec
	return nil
}

const recoveryContinuation = "Continue the same task from the current thread and work area. Preserve existing work and receipts. Do not repeat completed effects or reset review and CI allowances."

func (e *workerSession) transferRecoveryLease(turn string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, activity := range e.active {
		if strings.HasPrefix(key, "recovery:") && strings.TrimPrefix(key, "recovery:") != turn {
			e.active[turn] = activity
			delete(e.active, key)
		}
	}
}

func (r *conversationRuntime) workerRecoveryView(ctx context.Context, input ledger.Event) (*conversationRuntime, error) {
	// workerCompletionView expects a completion whose parent names the original
	// channel inbox. Keep the actual input unchanged for later result attribution.
	origin := input
	origin.ParentID = input.ID
	return r.workerCompletionView(ctx, origin)
}
