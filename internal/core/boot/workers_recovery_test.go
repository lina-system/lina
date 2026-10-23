// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
	"testing"
	"time"
)

func TestWorkerRecoveryDecisionIsDurableAndDoesNotSendInput(t *testing.T) {
	r := controlRuntime(t)
	if r.workers.recoveryStop != nil {
		r.workers.recoveryStop()
		<-r.workers.recoveryDone
		r.workers.recoveryStop = nil
	}
	r.ownerViews.stop()
	<-r.ownerViews.done
	rec := recovery.Record{ID: "thread", Person: r.person, Agent: r.agent, Input: "input", Conversation: r.conversation, Thread: worker.Thread{ID: "thread", TurnID: "second"}, State: "decision", Retries: 1, DecisionAt: time.Now().UnixNano()}
	if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	e := &workerSession{origin: r, thread: rec.Thread, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, recovery: rec, questions: map[string]*workerQuestion{}}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{rec.ID: e}}
	t.Cleanup(func() { r.workers = nil })
	e.recoveryDecision(rec)
	e.recoveryDecision(rec)
	source := workerWaitingSource{r: r, names: map[string]string{r.agent: "Lina"}}
	rows, err := source.Snapshot(t.Context(), r.person)
	if err != nil || len(rows) != 1 {
		t.Fatalf("decision rows=%+v %v", rows, err)
	}
	id, rev := "worker-request:recovery:thread", "1"
	action := "Keep paused"
	values := protocol.FormValues{"action": protocol.FormValue{String: &action}}
	q := workerQuestions{runtime: r}
	answer := protocol.QuestionAnswer{CardID: &id, Revision: &rev, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if outcome, err := q.AnswerQuestion(t.Context(), r.person, answer); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("decision=%s %v", outcome, err)
	}
	records, err := (recovery.Recovery{DB: r.db}).Records(t.Context())
	if err != nil || len(records) != 1 || records[0].State != "parked" || records[0].Retries != 1 || records[0].Decision != action {
		t.Fatalf("answer not durable: %+v %v", records, err)
	}
	// No adapter exists. Passing this test proves the answer did not send input.
}
func TestWorkerRecoveryBlocksInputWithUnsettledEffects(t *testing.T) {
	r, probe := runningWorkerFixture(t)
	probe.snapshot.State = worker.Idle
	e := r.workers.sessions["fixture"]
	for _, state := range []string{"unknown", "retrying", "decision", "parked"} {
		e.recovery.State = state
		if state == "unknown" {
			var reply workerResult
			if err := json.Unmarshal(e.encode(probe.snapshot).Body, &reply); err != nil || reply.State != worker.Unknown {
				t.Fatalf("snapshot hid unknown effects: %+v %v", reply, err)
			}
		}
		raw, _ := json.Marshal(workerArgs{Session: "fixture", Turn: "turn", Generation: 1, Binding: 1, Text: "continue"})
		result, err := r.workerOperation(t.Context(), "worker_input", "turn", ledger.Event{}, tools.Input{Arguments: raw})
		if err != nil || result.State != tools.Failed {
			t.Fatalf("%s accepted input: %+v %v", state, result, err)
		}
		item, err := observeRunningWorker(t.Context(), e)
		if state == "unknown" && (err != nil || item.State != "unknown") {
			t.Fatalf("idle summary hid uncertainty: %+v %v", item, err)
		}
	}
}

func TestWorkerInputRechecksRecoveryAfterControlRead(t *testing.T) {
	owner := &conversationRuntime{person: "person", agent: "agent", conversation: "main:agent"}
	thread := worker.Thread{ID: "thread", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	inputProbe := &workerProbe{}
	probe := &runningWorkerProbe{Adapter: inputProbe, snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}
	entry := &workerSession{adapter: probe, origin: owner, thread: thread, active: map[string]lease.Lease{}}
	entry.recovery.State = "running"
	owner.workers = &workerRuntime{sessions: map[string]*workerSession{thread.ID: entry}}
	probe.beforeSnapshot = func() {
		if entry.control.TryLock() {
			entry.control.Unlock()
			t.Error("input snapshot was not serialized with recovery")
		}
		// Learn about unresolved effects after the initial session lookup,
		// before the serialized control operation can admit worker input.
		entry.recoveryMu.Lock()
		entry.recovery.State = "unknown"
		entry.recoveryMu.Unlock()
	}
	raw, _ := json.Marshal(workerArgs{Session: thread.ID, Turn: thread.TurnID, Generation: 1, Binding: 1, Text: "continue"})
	result, err := owner.workerOperation(t.Context(), "worker_input", "turn", ledger.Event{}, tools.Input{Arguments: raw})
	if err != nil || result.State != tools.Failed || inputProbe.inputs != 0 {
		t.Fatalf("unsettled effects admitted input: state=%s inputs=%d err=%v", result.State, inputProbe.inputs, err)
	}
}

func TestWorkerRecoveryRequestResolutionCannotCrossBindings(t *testing.T) {
	r := controlRuntime(t)
	rec := recovery.Record{ID: "thread", Person: r.person, Agent: r.agent, Input: "input", Conversation: r.conversation, Thread: worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task", ProcessGeneration: 1, BindingEpoch: 1}, State: "unknown"}
	e := &workerSession{origin: r, thread: rec.Thread, recovery: rec}
	if err := e.trackRecoveryRequest("request-1", true); err != nil {
		t.Fatal(err)
	}
	oldRequestKey := e.requestKey("request-1")
	e.thread.BindingEpoch++
	if e.requestKey("request-1") == oldRequestKey {
		t.Fatal("approval key reused a retired binding")
	}
	e.recovery.Thread = e.thread
	if err := e.trackRecoveryRequest("request-1", false); err != nil {
		t.Fatal(err)
	}
	if len(e.recovery.PendingRequests) != 1 {
		t.Fatal("replacement process cleared an unobserved old request")
	}
}

type recoveryCompletionProbe struct{ workerProbe }

func (p *recoveryCompletionProbe) Reconcile(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return p.snapshot, nil
}
func TestWorkerRecoveryReleasesCompletedTurnActivity(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "reconcile-lease", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce result"})
	if err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &recoveryCompletionProbe{workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Idle, Finish: &worker.Finish{Status: worker.Done}, EffectsKnown: true}}}
	released := false
	activity := lease.New(func() { released = true })
	defer activity.Release()
	e := &workerSession{origin: r, input: input, thread: thread, adapter: probe, active: map[string]lease.Lease{thread.TurnID: activity}, recovery: recovery.Record{ID: thread.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Thread: thread, State: "unknown"}}
	e.reconcileWorker(t.Context())
	if e.recovery.State != "finished" || !released || len(e.active) != 0 || probe.inputs != 0 {
		t.Fatalf("reconciled completion retained activity: state=%s released=%v active=%d inputs=%d", e.recovery.State, released, len(e.active), probe.inputs)
	}
}

type failedRecoveryProbe struct {
	workerProbe
	events chan worker.Event
}

func (p *failedRecoveryProbe) Events() <-chan worker.Event { return p.events }
func TestWorkerRecoveryFailureRetainsUnknownDirectoryReservation(t *testing.T) {
	r := controlRuntime(t)
	thread := worker.Thread{ID: "failed-thread", TurnID: "failed-turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	dir := t.TempDir()
	const reservation = "unknown-worker"
	if err := r.work.ReserveWorker(t.Context(), reservation, dir); err != nil {
		t.Fatal(err)
	}
	probe := &failedRecoveryProbe{events: make(chan worker.Event, 2)}
	probe.events <- worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "tool_failed"}}
	probe.events <- worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}
	close(probe.events)
	e := &workerSession{origin: r, thread: thread, adapter: probe, workdir: dir, reservation: reservation, reservationTurn: thread.TurnID, active: map[string]lease.Lease{}, done: make(chan struct{}), close: func() error { return nil }, recovery: recovery.Record{ID: thread.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: "input", Thread: thread, State: "running", Reservation: reservation}}
	e.observe()
	if e.recovery.State != "unknown" {
		t.Fatalf("failure did not retain uncertainty: %s", e.recovery.State)
	}
	if err := r.work.CheckWorker(t.Context(), reservation); err != nil {
		t.Fatalf("unknown effects released directory reservation: %v", err)
	}
	item, err := observeRunningWorker(t.Context(), e)
	if err != nil || item.State != "failed" {
		t.Fatalf("known failure hidden by unknown effects: %+v %v", item, err)
	}

	records, err := (recovery.Recovery{DB: r.db}).Records(t.Context())
	if err != nil || len(records) != 1 || records[0].State != "unknown" || records[0].Reservation != reservation || records[0].Finish == nil || records[0].Finish.Status != worker.Failed {
		t.Fatalf("unknown reservation not durable: %+v %v", records, err)
	}
}

func TestDelayedWorkerFinishCannotSettleCurrentRecovery(t *testing.T) {
	r := controlRuntime(t)
	if r.workers.recoveryStop != nil {
		r.workers.recoveryStop()
		<-r.workers.recoveryDone
		r.workers.recoveryStop = nil
	}
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "late-finish", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Produce a result"})
	if err != nil {
		t.Fatal(err)
	}
	previous := worker.Thread{ID: "thread", TurnID: "previous", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	current := previous
	current.TurnID = "current"
	dir := t.TempDir()
	const reservation = "current-unknown"
	if err := r.work.ReserveWorker(t.Context(), reservation, dir); err != nil {
		t.Fatal(err)
	}
	probe := &failedRecoveryProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: current, State: worker.Idle}}, events: make(chan worker.Event, 3)}
	late := worker.Event{Kind: worker.FinishEvent, Thread: previous, Finish: &worker.Finish{Status: worker.Done}}
	probe.events <- worker.Event{Kind: worker.ProgressEvent, Thread: previous, Progress: &worker.Progress{Phase: "started"}}
	probe.events <- late
	probe.events <- late
	close(probe.events)
	rec := recovery.Record{ID: current.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID, Thread: current, State: "unknown", Reservation: reservation, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}}
	entry := &workerSession{origin: r, input: input, thread: current, adapter: probe, workdir: dir, reservation: reservation, reservationTurn: current.TurnID, active: map[string]lease.Lease{}, done: make(chan struct{}), close: func() error { return nil }, recovery: rec}
	if err := entry.recoveredFinish(t.Context(), late, "Previous result"); err != nil {
		t.Fatal(err)
	}
	store := recovery.Recovery{DB: r.db}
	if err := store.Put(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	entry.observe()
	records, err := store.Records(t.Context())
	if err != nil || len(records) != 1 || records[0].State != "unknown" || records[0].Thread != current || records[0].Finish.Status != worker.Failed {
		t.Fatalf("late prior completion settled current recovery: %+v %v", records, err)
	}
	if err := r.work.CheckWorker(t.Context(), reservation); err != nil {
		t.Fatalf("late completion released current reservation: %v", err)
	}
	got, err := r.recordedWorker(t.Context(), current.ID)
	if err != nil || got.Turn != previous.TurnID || got.Output != "Previous result" || got.Status != worker.Done {
		t.Fatalf("previous completion was lost or replaced: %+v %v", got, err)
	}
	r.workers.mu.Lock()
	r.workers.sessions[current.ID] = entry
	r.workers.mu.Unlock()
	raw, _ := json.Marshal(workerArgs{Session: current.ID, Turn: current.TurnID, Generation: 1, Binding: 1, Text: "continue"})
	result, err := r.workerOperation(t.Context(), "worker_input", "turn", input, tools.Input{Arguments: raw})
	if err != nil || result.State != tools.Failed || probe.inputs != 0 {
		t.Fatalf("late completion authorized new input: %+v inputs=%d err=%v", result, probe.inputs, err)
	}
}
