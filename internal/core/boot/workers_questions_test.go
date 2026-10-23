// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestACPSchemaInvalidSubmissionStaysOpen(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	probe := &scheduledAnswerProbe{}
	session := &workerSession{origin: r, adapter: probe, thread: worker.Thread{ID: "thread", ProcessGeneration: 1}, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, questions: map[string]*workerQuestion{}}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": session}}
	t.Cleanup(func() { r.workers = nil })
	params := json.RawMessage(`{"mode":"form","message":"Name","requestedSchema":{"type":"object","properties":{"label":{"type":"string","minLength":2}},"required":["label"]}}`)
	session.requestQuestion(worker.Event{Question: &worker.Request{ID: "req", Method: "elicitation/create", Params: params}})
	id := session.requestKey("req")
	short := "x"
	values := protocol.FormValues{"label": {String: &short}}
	revision := "1"
	outcome, err := (workerQuestions{runtime: r}).AnswerQuestion(t.Context(), r.person, protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values})
	var refusal *clientsrv.OperationError
	if outcome != "" || !errors.As(err, &refusal) || refusal.Code != protocol.ErrorMalformed || len(probe.answers) != 0 {
		t.Fatalf("schema miss settled the card: outcome=%s err=%v answers=%d", outcome, err, len(probe.answers))
	}
	session.mu.Lock()
	open := session.questions[id] != nil && session.questions[id].card.Card.Outcome == nil
	session.mu.Unlock()
	if !open {
		t.Fatal("invalid submission closed the card")
	}
	outcome, err = (workerQuestions{runtime: r}).AnswerQuestion(t.Context(), r.person, protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionDecline})
	if err != nil || outcome != protocol.DecisionOutcomeResolved || len(probe.answers) != 1 || !json.Valid(probe.answers[0].Result) {
		t.Fatalf("decline after invalid value: outcome=%s err=%v answers=%d", outcome, err, len(probe.answers))
	}
	if string(probe.answers[0].Result) != `{"action":"decline"}` {
		t.Fatalf("decline body: %s", probe.answers[0].Result)
	}
}
