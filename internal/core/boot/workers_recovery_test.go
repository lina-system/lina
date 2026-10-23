// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
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
