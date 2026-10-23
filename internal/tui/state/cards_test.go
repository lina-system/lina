// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func approvalCard() Card {
	return Card{ID: "approval", Epoch: "1", Revision: 1, Kind: protocol.KindApprovalRequest, Agent: "Agent", Recommendation: "Send", Summary: "Send document", Targets: []CardField{{Name: "recipient", Value: "recipient@example.invalid"}}, Choices: []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceDeny}}
}

func TestCardReplayAndFocusDoNotRingTwice(t *testing.T) {
	s := State{Epoch: "1"}
	s, cmds := Reduce(s, PutCard{Card: approvalCard(), Notify: true})
	if len(cmds) != 1 {
		t.Fatalf("first notification commands=%d", len(cmds))
	}
	if _, ok := cmds[0](context.Background()).(CardBell); !ok {
		t.Fatal("missing bell intent")
	}
	before := s
	s, cmds = Reduce(s, PutCard{Card: approvalCard(), Notify: true})
	if len(cmds) != 0 || !reflect.DeepEqual(before, s) {
		t.Fatal("replay changed state or rang")
	}
	focused, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
	_, cmds = Reduce(focused, PutCard{Card: approvalCard(), Notify: true})
	if len(cmds) != 0 {
		t.Fatal("previously displayed approval rang")
	}
}

func TestCardConfirmRequiresSelectionAndCompleteTargets(t *testing.T) {
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
	s, _ = Reduce(s, OpenCard{ID: "approval"})
	_, cmds := Reduce(s, ConfirmCard{})
	if len(cmds) != 0 {
		t.Fatal("confirmed without choice")
	}
	s, _ = Reduce(s, ChooseApproval{Choice: protocol.ApprovalChoiceOnce})
	_, cmds = Reduce(s, ConfirmCard{})
	if len(cmds) != 0 {
		t.Fatal("confirmed cut target")
	}
	s, _ = Reduce(s, CardTargetsVisible{Complete: true})
	s, cmds = Reduce(s, ConfirmCard{})
	if len(cmds) != 1 {
		t.Fatalf("decision commands=%d", len(cmds))
	}
	req, ok := cmds[0](context.Background()).(CardAnswerRequest)
	if !ok || req.ID != "approval" || req.Revision != 1 || req.Choice != protocol.ApprovalChoiceOnce {
		t.Fatalf("request=%+v", req)
	}
	if _, cmds = Reduce(s, ConfirmCard{}); len(cmds) != 0 {
		t.Fatal("duplicate confirmation")
	}
}

func TestChangedGroupClearsSelectionAndRequiresRedisplay(t *testing.T) {
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
	s, _ = Reduce(s, OpenCard{ID: "approval"})
	s, _ = Reduce(s, ChooseApproval{Choice: protocol.ApprovalChoiceOnce})
	s, _ = Reduce(s, CardTargetsVisible{Complete: true})
	newer := approvalCard()
	newer.Revision = 2
	newer.Targets = append(newer.Targets, CardField{Name: "recipient", Value: "second@example.invalid"})
	s, _ = Reduce(s, PutCard{Card: newer, Notify: true})
	if s.CardView.Choice != "" || s.CardView.TargetsVisible {
		t.Fatal("changed group retained confirmation")
	}
	if _, cmds := Reduce(s, ConfirmCard{}); len(cmds) != 0 {
		t.Fatal("changed group approved")
	}
}

func TestCardOutcomesEpochAndEscapeNeverDecide(t *testing.T) {
	for _, outcome := range []CardOutcome{CardResolved, CardAlreadyResolved, CardWithdrawn, CardExpired, CardStaleEpoch, CardStaleGroup, CardRefused, CardStopped} {
		t.Run(string(outcome), func(t *testing.T) {
			s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
			s, _ = Reduce(s, OpenCard{ID: "approval"})
			s, _ = Reduce(s, ChooseApproval{Choice: protocol.ApprovalChoiceOnce})
			s, _ = Reduce(s, CardTargetsVisible{Complete: true})
			s, cmds := Reduce(s, ConfirmCard{})
			if len(cmds) != 1 {
				t.Fatalf("decision commands=%d", len(cmds))
			}
			req := cmds[0](context.Background()).(CardAnswerRequest)
			old := s
			s, _ = Reduce(s, CardAnswerResult{Request: req, Outcome: outcome})
			if s.Cards[0].Outcome != outcome || s.CardView.Pending || s.CardView.Choice != "" {
				t.Fatalf("outcome=%+v", s)
			}
			if old.Cards[0].Outcome != "" {
				t.Fatal("reducer mutated input")
			}
			if _, cmds := Reduce(s, ConfirmCard{}); len(cmds) != 0 {
				t.Fatal("terminal card answered")
			}
		})
	}
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
	s, _ = Reduce(s, OpenCard{ID: "approval"})
	s, cmds := Reduce(s, CloseCard{})
	if len(cmds) != 0 || s.CardView.ID != "" || s.Cards[0].Outcome != "" {
		t.Fatal("escape decided approval")
	}
	s, _ = Reduce(s, Snapshot{Epoch: "2", Revocation: "1", Topic: "waiting", Cursor: 1})
	if len(s.Cards) != 1 {
		t.Fatalf("cards=%d", len(s.Cards))
	}
	if s.Cards[0].Outcome != CardStopped {
		t.Fatal("restart did not stop approval")
	}
}

func TestCardForeignTextSanitizedAndInputsOwned(t *testing.T) {
	card := approvalCard()
	card.Targets[0].Value = "a\x1b]52;c;secret\a\u202eb"
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: card})
	card.Targets[0].Value = "mutated"
	card.Choices[0] = protocol.ApprovalChoiceDeny
	if len(s.Cards) != 1 {
		t.Fatalf("cards=%d", len(s.Cards))
	}
	if s.Cards[0].Targets[0].Value != "ab" || s.Cards[0].Choices[0] != protocol.ApprovalChoiceOnce {
		t.Fatal("unsafe or aliased input")
	}
}

func TestQuestionPromptsValidateTypedValuesAndNeverApprove(t *testing.T) {
	c := approvalCard()
	c.Kind = protocol.KindQuestion
	c.Choices = nil
	choices := []string{"first", "second"}
	c.Form = &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "name", Type: protocol.FormFieldTypeText}, {Name: "count", Type: protocol.FormFieldTypeNumber}, {Name: "enabled", Type: protocol.FormFieldTypeBoolean}, {Name: "option", Type: protocol.FormFieldTypeChoice, Choices: &choices}}}
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: c})
	s, _ = Reduce(s, OpenCard{ID: c.ID})
	if _, cmds := Reduce(s, ConfirmCard{}); len(cmds) != 0 {
		t.Fatal("question became approval")
	}
	if _, cmds := Reduce(s, AnswerQuestion{Action: protocol.QuestionAnswerActionAccept}); len(cmds) != 0 {
		t.Fatal("incomplete form accepted")
	}
	for name, value := range map[string]string{"name": "draft", "count": "invalid", "enabled": "true", "option": "first"} {
		s, _ = Reduce(s, EditQuestionField{Name: name, Value: value})
	}
	if _, cmds := Reduce(s, AnswerQuestion{Action: protocol.QuestionAnswerActionAccept}); len(cmds) != 0 {
		t.Fatal("invalid number accepted")
	}
	s, _ = Reduce(s, EditQuestionField{Name: "count", Value: "9007199254740993"})
	_, cmds := Reduce(s, AnswerQuestion{Action: protocol.QuestionAnswerActionAccept})
	if len(cmds) != 1 {
		t.Fatal("valid form rejected")
	}
	req := cmds[0](context.Background()).(CardAnswerRequest)
	if req.Choice != "" || req.QuestionAction != protocol.QuestionAnswerActionAccept || string(*req.Values["count"].Number) != "9007199254740993" {
		t.Fatalf("typed question=%+v", req)
	}
}

func TestReconnectRequiresFreshSelectionWithoutReplayingAnswer(t *testing.T) {
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: approvalCard()})
	s, _ = Reduce(s, OpenCard{ID: "approval"})
	s, _ = Reduce(s, ChooseApproval{Choice: protocol.ApprovalChoiceOnce})
	s, _ = Reduce(s, CardTargetsVisible{Complete: true})
	s, cmds := Reduce(s, ConfirmCard{})
	request := cmds[0](context.Background()).(CardAnswerRequest)
	s, cmds = Reduce(s, ResetCardRequests{})
	if len(cmds) != 0 || s.CardView.Pending || s.CardView.Choice != "" {
		t.Fatal("reconnect retained or replayed answer")
	}
	after, _ := Reduce(s, CardAnswerResult{Request: request, Outcome: CardResolved})
	if after.Cards[0].Outcome != "" {
		t.Fatal("old connection result accepted")
	}
}

func TestQuestionNumberPromptRejectsNonNumbers(t *testing.T) {
	form := &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "count", Type: protocol.FormFieldTypeNumber}}}
	for _, value := range []string{"null", "true", `"12"`, "[]", "{}", "NaN", "1e", "01"} {
		if got, ok := QuestionValues(form, map[string]string{"count": value}); ok {
			t.Fatalf("accepted non-number %q as %+v", value, got)
		}
	}
	for _, value := range []string{"9007199254740993", "1e1000", "-0.25"} {
		got, ok := QuestionValues(form, map[string]string{"count": value})
		if !ok || string(*got["count"].Number) != value {
			t.Fatalf("changed exact number %q: %+v", value, got)
		}
	}
}

func TestOptionalQuestionFieldMayBeBlank(t *testing.T) {
	optional := true
	form := &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "label", Type: protocol.FormFieldTypeText}, {Name: "limit", Type: protocol.FormFieldTypeNumber, Optional: &optional}}}
	got, ok := QuestionValues(form, map[string]string{"label": "ok", "limit": ""})
	_, present := got["limit"]
	if !ok || got["label"].String == nil || *got["label"].String != "ok" || present {
		t.Fatalf("blank optional was submitted: %+v %v", got, ok)
	}
	if _, ok = QuestionValues(form, map[string]string{"limit": "1"}); ok {
		t.Fatal("blank required field accepted")
	}
}

func TestResolvedPublicationKeepsTheCorrelatedRaceOutcome(t *testing.T) {
	card := approvalCard()
	card.Token = "group"
	s, _ := Reduce(State{Epoch: "1"}, PutCard{Card: card})
	s, _ = Reduce(s, OpenCard{ID: card.ID})
	s, _ = Reduce(s, ChooseApproval{Choice: protocol.ApprovalChoiceOnce})
	s, _ = Reduce(s, CardTargetsVisible{Complete: true})
	s, cmds := Reduce(s, ConfirmCard{})
	request := cmds[0](context.Background()).(CardAnswerRequest)
	card.Revision++
	card.Outcome = CardResolved
	s, _ = Reduce(s, PutCard{Card: card})
	s, _ = Reduce(s, CardAnswerResult{Request: request, Outcome: CardAlreadyResolved})
	if s.Cards[0].Outcome != CardAlreadyResolved {
		t.Fatal("resolved publication hid the losing client's correlated outcome")
	}
}
