// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/kit/protocol"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"strings"
)

func runRuntime(t *testing.T) *conversationRuntime {
	t.Helper()
	r := controlRuntime(t)
	s, err := runs.New(runs.Config{Epoch: r.epoch, DB: r.db, Assets: r.assets, AuthorizeFollowUp: r.authorizeRunFollowSource, WorkDir: filepath.Join(r.root, "work"), Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, PublishPromotion: func(context.Context, *canon.Tx, string, budget.Handle, budget.Handoff) error { return nil }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("unexpected model request")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r.background = &backgroundRuntime{service: s, list: runs.NewRunningList(r.db, map[string]runs.ListSource{"run": runs.RunListSource{}}), notices: lane.NewNotices(r.db, nil), published: map[string]string{}}
	if err = r.lanes.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.lanes = lane.NewWithNotices(r.background.notices)
	return r
}

func TestEffectQuestionReachesConversationAtBoundary(t *testing.T) {
	r := runRuntime(t)
	deliveredEvent := make(chan lane.Delivery, 1)
	r.background.notices.OnDelivered = func(d lane.Delivery) {
		if err := r.publishNoticeText(t.Context(), d.ID, d.Notices); err != nil {
			t.Error(err)
		}
		deliveredEvent <- d
	}
	started, finish := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	turn, err := r.lanes.Submit(t.Context(), r.conversation, func(context.Context) error { close(started); <-finish; return nil })
	if err != nil {
		t.Fatal(err)
	}
	<-started
	b := runs.Brief{Goal: "finish request", Scope: "reads", NotTouch: "effects", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	h, err := r.background.service.StartFromTurn(t.Context(), r.runOrigin("effect-turn"), b, budget.Handoff{Turn: "effect-turn", Input: "whole request", Effects: []budget.Effect{{CallID: "send", Key: "effect-key", Tool: "send", Code: loop.Unknown}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = r.syncRuns(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := r.background.service.EffectQuestions(t.Context(), r.conversation)
	if err != nil || len(pending) != 0 {
		t.Fatalf("effect question was not transferred: %+v err=%v", pending, err)
	}
	delivered, err := r.background.notices.Delivered(t.Context(), r.conversation)
	if err != nil || len(delivered) != 0 {
		t.Fatalf("question interrupted turn: %+v %v", delivered, err)
	}
	close(finish)
	if err = <-turn; err != nil {
		t.Fatal(err)
	}
	select {
	case <-deliveredEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("notice delivery did not finish")
	}
	delivered, err = r.background.notices.Delivered(t.Context(), r.conversation)
	if err != nil || len(delivered) != 1 || len(delivered[0].Notices) != 1 || delivered[0].Notices[0].Run != h.ID || delivered[0].Notices[0].Outcome != "needs_input" {
		t.Fatalf("conversation question=%+v %v", delivered, err)
	}
	snapshot, subscription, err := r.topics.Subscribe("waiting", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var question protocol.Question
	for _, item := range snapshot.Snapshot.Items {
		if item.Kind == protocol.KindQuestion {
			if err := json.Unmarshal(item.Data, &question); err != nil {
				t.Fatal(err)
			}
		}
	}
	if question.Card == nil || question.Form == nil {
		t.Fatal("effect question has no answerable card")
	}
	ready, drain := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-drain:
		default:
			close(drain)
		}
	}()
	directed := make(chan []provider.Item, 1)
	active, err := r.lanes.SubmitLoop(t.Context(), r.conversation, func(_ context.Context, inputs *loop.Inputs) error {
		close(ready)
		<-drain
		directed <- inputs.Drain()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ready
	direction := "Continue with analysis only; leave the unknown action untouched."
	values := protocol.FormValues{"Direction": protocol.FormValue{String: &direction}}
	answer := protocol.QuestionAnswer{CardID: &question.Card.ID, Revision: &question.Card.Revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	operation := r.identity.Operations[protocol.KindQuestionAnswer]
	session := clientsrv.Session{PersonID: r.person, CanDecide: false, Capabilities: []protocol.Capability{clientsrv.DecisionsCapability}}
	if _, err = operation.Handle(t.Context(), session, protocol.Envelope{Payload: answer}); err == nil {
		t.Fatal("untrusted caller answered effect question")
	}
	session.CanDecide = true
	response, err := operation.Handle(t.Context(), session, protocol.Envelope{Payload: answer})
	if err != nil || *response.Payload.(protocol.QuestionAnswer).Outcome != protocol.DecisionOutcomeResolved {
		t.Fatal("authenticated answer refused", err)
	}
	pendingAnswer, err := r.background.service.Get(t.Context(), h.ID)
	if err != nil || pendingAnswer.EffectsQuestion.Answer == nil {
		t.Fatal("answer not durable", err)
	}
	for range 2 {
		if err = r.deliverEffectAnswer(t.Context(), pendingAnswer); err != nil {
			t.Fatal(err)
		}
	}
	close(drain)
	if err = <-active; err != nil {
		t.Fatal(err)
	}
	input := <-directed
	if len(input) != 1 || !strings.Contains(string(input[0]), direction) {
		t.Fatalf("answer not delivered exactly once to conversation: %s", input)
	}
	record, err := r.background.service.Get(t.Context(), h.ID)
	if err != nil || record.EffectsQuestion == nil || record.EffectsQuestion.Effects[0].Code != loop.Unknown || !record.EffectsQuestion.Answer.Delivered {
		t.Fatal("question delivery changed effect authority", err)
	}
}

func TestRunNoticeRestoresWithoutConfiguredModel(t *testing.T) {
	r := controlRuntime(t)
	if r.background != nil {
		t.Fatal("fixture unexpectedly configured a model")
	}
	if err := r.publishNoticeText(t.Context(), "retained-notice", []lane.Notice{{ID: "retained-notice", Run: "retained-run", Summary: "Retained report", Result: "retained-material", ResultRevision: 1}}); err != nil {
		t.Fatal(err)
	}
}

func TestDeletedEffectRecipientDoesNotBlockOtherRunNotices(t *testing.T) {
	r := runRuntime(t)
	b := runs.Brief{Goal: "finish request", Scope: "reads", NotTouch: "effects", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	h, err := r.background.service.StartFromTurn(t.Context(), r.runOrigin("deleted-effect"), b, budget.Handoff{Turn: "deleted-effect", Input: "whole request", Effects: []budget.Effect{{CallID: "send", Key: "send-key", Tool: "send", Code: loop.Unknown}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.background.service.AnswerEffectQuestion(t.Context(), r.person, h.ID, "accept", "Continue reading only"); err != nil {
		t.Fatal(err)
	}
	if err = r.agents.Delete(t.Context(), r.agent, r.person, time.Now()); err != nil {
		t.Fatal(err)
	}
	other, err := r.agents.Create(t.Context(), agents.Create{Name: "Other", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	origin := r.runOrigin("unrelated")
	origin.Agent, origin.Conversation = other.ID, "main:"+other.ID
	id, _, err := r.background.service.Start(t.Context(), origin, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.background.service.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	deliveries := make(chan lane.Delivery, 4)
	r.background.notices.OnDelivered = func(d lane.Delivery) { deliveries <- d }
	for range 2 {
		if err = r.syncRuns(t.Context()); err != nil {
			t.Fatalf("unavailable recipient stopped global sync: %v", err)
		}
	}
	for seen, questionSeen := false, false; !seen || !questionSeen; {
		select {
		case d := <-deliveries:
			for _, n := range d.Notices {
				if n.Run == id {
					seen = true
				}
				if n.Run == h.ID {
					questionSeen = true
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("unrelated landing blocked")
		}
	}
	record, err := r.background.service.Get(t.Context(), h.ID)
	if err != nil || record.EffectsQuestion.Answer == nil || record.EffectsQuestion.Answer.Delivered || record.EffectsQuestion.Effects[0].Code != loop.Unknown {
		t.Fatal("undelivered answer or unknown effect lost", err)
	}
	// Already delivered cards cannot accept new direction after their agent is deleted.
	key := h.ID + ":effects"
	q, err := (runQuestions{runtime: r}).Question(t.Context(), r.person, key)
	if err != nil || q.Card.Outcome == nil || *q.Card.Outcome != protocol.DecisionOutcomeWithdrawn {
		t.Fatalf("deleted recipient card=%+v err=%v", q, err)
	}
}
