// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/protocol"
)

func decisionFixture(t *testing.T) (*Decisions, *canon.Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("unused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewTopics("1", TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hub.Close() })
	return &Decisions{Authority: svc, Topics: hub, Person: "person", AgentName: func(string) string { return "Agent" }}, db
}
func seedDecision(t *testing.T, db *canon.Store, id string) {
	t.Helper()
	a := authority.Approval{ID: id, EffectKey: id, What: "Send document", Where: "recipient@example.invalid", Target: "document", ScopeKey: "send", Arguments: json.RawMessage(`{"body":"document"}`), Epoch: 1, Person: "person", Agent: "agent", State: authority.Pending, Outward: true, Choices: []authority.Choice{authority.Once, authority.Deny}}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", id, id, id, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func decisionSession(id string) Session {
	return Session{ConnectionID: id, ClientID: id, PersonID: "person", Epoch: 1, CanDecide: true, Capabilities: []protocol.Capability{DecisionsCapability}}
}
func TestDecisionClientsRaceExactlyOneResolved(t *testing.T) {
	d, db := decisionFixture(t)
	seedDecision(t, db, "approval")
	cards, err := d.ApprovalCards(t.Context())
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards=%+v err=%v", cards, err)
	}
	card := cards[0].Card
	answer := protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce, CardID: &card.ID, Revision: &card.Revision}
	start := make(chan struct{})
	results := make(chan protocol.DecisionOutcome, 2)
	var wg sync.WaitGroup
	for _, client := range []string{"first", "second"} {
		wg.Go(func() {
			<-start
			r, err := d.Operations()[protocol.KindApprovalAnswer].Handle(t.Context(), decisionSession(client), protocol.Envelope{Kind: protocol.KindApprovalAnswer, Payload: answer})
			if err != nil {
				t.Error(err)
				results <- ""
				return
			}
			results <- *r.Payload.(protocol.ApprovalAnswer).Outcome
		})
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[protocol.DecisionOutcome]int{}
	for result := range results {
		counts[result]++
	}
	if counts[protocol.DecisionOutcomeResolved] != 1 || counts[protocol.DecisionOutcomeAlreadyResolved] != 1 {
		t.Fatalf("outcomes=%v", counts)
	}
}
func TestDecisionChangedGroupReturnsStaleWithoutPartialApproval(t *testing.T) {
	d, db := decisionFixture(t)
	seedDecision(t, db, "first")
	cards, err := d.ApprovalCards(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	card := cards[0].Card
	seedDecision(t, db, "second")
	r, err := d.Operations()[protocol.KindApprovalAnswer].Handle(t.Context(), decisionSession("client"), protocol.Envelope{Kind: protocol.KindApprovalAnswer, Payload: protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce, CardID: &card.ID, Revision: &card.Revision}})
	if err != nil || *r.Payload.(protocol.ApprovalAnswer).Outcome != protocol.DecisionOutcomeStaleGroup {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	pending, err := d.Authority.Pending(t.Context())
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	cards, err = d.ApprovalCards(t.Context())
	if err != nil || len(cards) != 1 {
		t.Fatalf("scope was not one card: %+v %v", cards, err)
	}
	card = cards[0].Card
	r, err = d.Operations()[protocol.KindApprovalAnswer].Handle(t.Context(), decisionSession("client"), protocol.Envelope{Kind: protocol.KindApprovalAnswer, Payload: protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceDeny, CardID: &card.ID, Revision: &card.Revision}})
	if err != nil || *r.Payload.(protocol.ApprovalAnswer).Outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	pending, err = d.Authority.Pending(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}
func TestDecisionRejectsUntrustedAndUncorrelatedAnswers(t *testing.T) {
	d, db := decisionFixture(t)
	seedDecision(t, db, "approval")
	cards, err := d.ApprovalCards(t.Context())
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards=%v err=%v", cards, err)
	}
	c := cards[0].Card
	for _, test := range []struct {
		name    string
		session Session
		answer  protocol.ApprovalAnswer
	}{
		{"free text", decisionSession("client"), protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce}},
		{"descendant", Session{PersonID: "person", Epoch: 1}, protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce, CardID: &c.ID, Revision: &c.Revision}},
		{"wrong owner", Session{PersonID: "other", Epoch: 1, CanDecide: true}, protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce, CardID: &c.ID, Revision: &c.Revision}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := d.Operations()[protocol.KindApprovalAnswer].Handle(t.Context(), test.session, protocol.Envelope{Kind: protocol.KindApprovalAnswer, Payload: test.answer})
			if err == nil {
				t.Fatal("invalid answer accepted")
			}
		})
	}
	pending, _ := d.Authority.Pending(t.Context())
	if len(pending) != 1 {
		t.Fatal("refusal mutated approval")
	}
}

type formSource struct {
	question protocol.Question
	calls    int
}

func (s *formSource) Question(context.Context, string, string) (protocol.Question, error) {
	return s.question, nil
}
func (s *formSource) AnswerQuestion(_ context.Context, _ string, _ protocol.QuestionAnswer) (protocol.DecisionOutcome, error) {
	s.calls++
	return protocol.DecisionOutcomeResolved, nil
}
func TestQuestionHandlerUsesSourceAndRefusesSecretForms(t *testing.T) {
	d, _ := decisionFixture(t)
	source := &formSource{question: protocol.Question{ConversationID: "conversation", Card: &protocol.DecisionCard{ID: "question", Revision: "revision", Agent: "Agent", Summary: "Choose count", Targets: []protocol.DecisionTarget{}}, Form: &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "count", Type: protocol.FormFieldTypeNumber}}}}}
	d.Questions = source
	id, revision := "question", "revision"
	number := json.Number("9007199254740993")
	values := protocol.FormValues{"count": {Number: &number}}
	answer := protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	result, err := d.Operations()[protocol.KindQuestionAnswer].Handle(t.Context(), decisionSession("client"), protocol.Envelope{Kind: protocol.KindQuestionAnswer, Payload: answer})
	if err != nil || *result.Payload.(protocol.QuestionAnswer).Outcome != protocol.DecisionOutcomeResolved || source.calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, source.calls, err)
	}
	source.question.Form.Fields[0].Name = "api_key"
	result, err = d.Operations()[protocol.KindQuestionAnswer].Handle(t.Context(), decisionSession("client"), protocol.Envelope{Kind: protocol.KindQuestionAnswer, Payload: answer})
	if err != nil || *result.Payload.(protocol.QuestionAnswer).Outcome != protocol.DecisionOutcomeRefused || source.calls != 1 {
		t.Fatalf("secret reached source: %+v %v", result, err)
	}
	if err := d.PublishQuestion(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionGroupRollsBackEarlierMemberWhenLaterChoiceRefused(t *testing.T) {
	d, db := decisionFixture(t)
	seedDecision(t, db, "first")
	seedDecision(t, db, "second")
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		a, err := json.Marshal([]authority.Choice{authority.Deny})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "UPDATE authority_approvals SET record=json_set(record,'$.Choices',json(?)) WHERE approval_id='second'", string(a))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	groups, err := d.Authority.Groups(t.Context(), "person")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%v %v", groups, err)
	}
	err = d.Authority.Answer(t.Context(), groups[0].ID, authority.Decider{Person: "person", Epoch: 1, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: authority.Once, GroupRevision: groups[0].Revision})
	if !errors.Is(err, authority.ErrChoice) {
		t.Fatalf("answer=%v", err)
	}
	pending, err := d.Authority.Pending(t.Context())
	if err != nil || len(pending) != 2 {
		t.Fatalf("partial group: %+v %v", pending, err)
	}
	cards, err := d.ApprovalCards(t.Context())
	if err != nil || len(cards) != 1 || len(cards[0].Choices) != 1 || cards[0].Choices[0] != protocol.ApprovalChoiceDeny {
		t.Fatalf("choice intersection=%+v %v", cards, err)
	}
}
func TestDecisionChangedArgumentsInvalidateDisplayedRevision(t *testing.T) {
	d, db := decisionFixture(t)
	seedDecision(t, db, "approval")
	cards, err := d.ApprovalCards(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	card := cards[0].Card
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE authority_approvals SET record=json_set(record,'$.Where',?) WHERE approval_id=?", "changed@example.invalid", card.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := d.approvalAnswer(t.Context(), decisionSession("client"), protocol.Envelope{Payload: protocol.ApprovalAnswer{CardID: &card.ID, Revision: &card.Revision, Choice: protocol.ApprovalChoiceOnce}})
	if err != nil || *result.Payload.(protocol.ApprovalAnswer).Outcome != protocol.DecisionOutcomeStaleGroup {
		t.Fatalf("changed arguments=%+v %v", result, err)
	}
	pending, _ := d.Authority.Pending(t.Context())
	if len(pending) != 1 {
		t.Fatal("changed target approved")
	}
}
