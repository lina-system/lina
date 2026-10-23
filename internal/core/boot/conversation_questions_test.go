// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func questionPersonInput(t *testing.T, r *conversationRuntime, id, text string) ledger.Event {
	t.Helper()
	now := time.Now()
	e, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: ledger.Surface{Kind: "tui", Language: "en"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func questionBindings(t *testing.T, r *conversationRuntime, input ledger.Event, method agents.WorkMethodSettings) (map[string]loop.Tool, *questionTurn) {
	t.Helper()
	bindings := map[string]loop.Tool{}
	gate := &questionTurn{}
	item, err := r.prepareQuestionTool(t.Context(), "turn:"+input.ID, input, method, bindings, gate)
	if err != nil {
		t.Fatal(err)
	}
	var packet struct{ Ask askPreference }
	if json.Unmarshal([]byte(item.Text), &packet) != nil || packet.Ask.PerTurn < 1 || packet.Ask.Unanswered != "default" {
		t.Fatalf("working preference: %s", item.Text)
	}
	return bindings, gate
}
func questionCall(t *testing.T, bindings map[string]loop.Tool, name, id string, args any) loop.Result {
	t.Helper()
	raw, _ := json.Marshal(args)
	call := provider.ToolCall{Name: name, ID: id, Arguments: string(raw)}
	tool := bindings[name]
	if _, err := tool.Validate(call); err != nil {
		return loop.Result{ID: id, Code: loop.Invalid}
	}
	return tool.Execute(t.Context(), call)
}
func TestQuestionPreparedFillAndTurnLimit(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "ask", "Prepare a draft")
	pin := agents.Preparation{WorkMethod: `{"schema":1,"ask":{"per_turn":1},"fill":["ask.per_turn","ask.form"]}`}
	state := agents.State{WorkMethodFills: map[string]agents.WorkMethodFill{"ask.per_turn": {Value: json.RawMessage(`2`), Source: input.ID}, "ask.form": {Value: json.RawMessage(`"choices"`), Source: input.ID}}}
	method := agents.ProjectWorkMethod(pin, state)
	b, _ := questionBindings(t, r, input, method)
	args := waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Default: "table"}
	for _, id := range []string{"one", "two"} {
		if got := questionCall(t, b, "ask_question", id, args); got.Code != loop.OK {
			t.Fatalf("question %s: %+v", id, got)
		}
	}
	method.Values["ask.per_turn"] = json.RawMessage(`99`)
	if got := questionCall(t, b, "ask_question", "three", args); got.Code != loop.Invalid {
		t.Fatalf("prepared limit changed: %+v", got)
	}
	if got := questionCall(t, b, "ask_question", "one", args); got.Code != loop.OK {
		t.Fatalf("retry consumed slot: %+v", got)
	}
}
func TestQuestionRelatedDefault(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "ask", "Prepare a draft")
	method := agents.ProjectWorkMethod(agents.Preparation{}, agents.State{})
	b, _ := questionBindings(t, r, input, method)
	result := questionCall(t, b, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Choices: []string{"table", "list"}, Default: "table"})
	var q waiting.ConversationQuestion
	if json.Unmarshal(result.Output, &q) != nil || q.ID == "" {
		t.Fatalf("question: %+v", result)
	}
	related := questionPersonInput(t, r, "followup", "Why a table?")
	b, g := questionBindings(t, r, related, method)
	if g.check(t.Context()) == nil {
		t.Fatal("unclassified question allowed completion")
	}
	if g.before(t.Context(), provider.ToolCall{Name: "write_record", ID: "write"}) == nil {
		t.Fatal("unclassified question allowed work")
	}
	for _, quote := range []string{"", "unseen person words"} {
		if got := questionCall(t, b, "resolve_question", "bad", map[string]string{"id": q.ID, "kind": "default_applied", "quote": quote}); got.Code != loop.Invalid {
			t.Fatalf("ungrounded decision accepted: %+v", got)
		}
	}
	if got := questionCall(t, b, "resolve_question", "related", map[string]string{"id": q.ID, "kind": "related", "quote": related.Body.Text()}); got.Code != loop.OK {
		t.Fatalf("related: %+v", got)
	}
	if err := g.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, _ := waiting.NewConversationQuestions(r.db).Get(t.Context(), q.ID)
	if saved.Resolution != nil {
		t.Fatal("related input closed question")
	}
	unrelated := questionPersonInput(t, r, "next", "Tell me the time")
	b, g = questionBindings(t, r, unrelated, method)
	for _, id := range []string{"resolve", "retry"} {
		if got := questionCall(t, b, "resolve_question", id, map[string]string{"id": q.ID, "kind": "default_applied", "quote": unrelated.Body.Text()}); got.Code != loop.OK {
			t.Fatalf("default: %+v", got)
		}
	}
	if err := g.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), q.ID)
	if err != nil || saved.Resolution == nil || saved.Resolution.Kind != "default_applied" || saved.Resolution.Value != "table" || saved.Resolution.Input != unrelated.ID {
		t.Fatalf("default receipt: %+v %v", saved, err)
	}
	var notices int
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE scaffold_kind='question_default'").Scan(&notices)
	}); err != nil || notices != 1 {
		t.Fatalf("notices=%d err=%v", notices, err)
	}
	history, err := r.historyItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restored := false
	for _, item := range history {
		restored = restored || item.eventID == q.ID+":default:"+unrelated.ID
	}
	if !restored {
		t.Error("default notice absent from durable display history")
	}
	events, err := r.events(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, event := range events {
		retained = retained || event.ScaffoldKind == "question_default"
	}
	if !retained {
		t.Error("default decision absent from model history")
	}
	card, err := (workerQuestions{r}).Question(t.Context(), r.person, q.ID)
	if err != nil || card.Card.Outcome == nil {
		t.Fatalf("card not closed: %+v %v", card, err)
	}
}
func TestQuestionCardAnswers(t *testing.T) {
	for _, action := range []protocol.QuestionAnswerAction{protocol.QuestionAnswerActionAccept, protocol.QuestionAnswerActionDecline} {
		t.Run(string(action), func(t *testing.T) {
			r := controlRuntime(t)
			input := questionPersonInput(t, r, "ask", "Prepare a draft")
			b, _ := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
			result := questionCall(t, b, "ask_question", "format", waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Choices: []string{"table", "list"}, Default: "table"})
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(result.Output, &q)
			revision := "1"
			value := "list"
			values := protocol.FormValues{"answer": {String: &value}}
			answer := protocol.QuestionAnswer{CardID: &q.ID, Revision: &revision, Answer: action, Values: &values}
			bridge := workerQuestions{r}
			if outcome, err := bridge.AnswerQuestionMessage(t.Context(), "other", answer, "transport-id"); err != nil || outcome != protocol.DecisionOutcomeRefused {
				t.Fatalf("owner check: %s %v", outcome, err)
			}
			if outcome, err := bridge.AnswerQuestionMessage(t.Context(), r.person, answer, "transport-id"); err != nil || outcome != protocol.DecisionOutcomeResolved {
				t.Fatalf("answer: %s %v", outcome, err)
			}
			q, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), q.ID)
			var deliveries int
			if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox i JOIN conv_event e ON e.id=i.native_id WHERE i.source='question-answer' AND e.id=?", q.ID+":answer").Scan(&deliveries)
			}); err != nil || deliveries != 1 {
				t.Errorf("answer has no durable conversation delivery: %d %v", deliveries, err)
			}
			events, eventsErr := r.events(t.Context(), "")
			if eventsErr != nil {
				t.Fatal(eventsErr)
			}
			retained := false
			for _, event := range events {
				retained = retained || event.ScaffoldKind == "question_answer"
			}
			if !retained {
				t.Error("card answer absent from model history")
			}
			kind := "answered"
			if action == protocol.QuestionAnswerActionDecline {
				kind = "declined"
				value = ""
			}
			if err != nil || q.Resolution == nil || q.Resolution.Kind != kind || q.Resolution.Value != value || q.Resolution.Input != "transport-id" {
				t.Fatalf("answer receipt: %+v %v", q, err)
			}
		})
	}
}

func TestQuestionForgottenInput(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "ask-private", "Prepare a private draft")
	method := agents.ProjectWorkMethod(agents.Preparation{}, agents.State{})
	b, _ := questionBindings(t, r, input, method)
	if result := questionCall(t, b, "ask_question", "private", waiting.QuestionDraft{Question: "Private topic?", Recommendation: "Private default", Default: "Private default"}); result.Code != loop.OK {
		t.Fatal(result)
	}
	remembered, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Agent: r.agent, Person: r.person, Item: memory.Item{Kind: memory.Preference, Content: input.Body.Text(), Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: input.ID, Precision: memory.Exact}}}})
	if err != nil || !remembered.Remembered() {
		t.Fatalf("remember fixture: %+v %v", remembered, err)
	}
	if err := r.removals.Forget(t.Context(), retract.Request{Person: r.person, Targets: []retract.Ref{{Kind: retract.Memory, ID: remembered.ID}}}); err != nil {
		t.Fatal(err)
	}
	next := questionPersonInput(t, r, "next", "New topic")
	gate := &questionTurn{}
	packet, err := r.prepareQuestionTool(t.Context(), "next-turn", next, method, map[string]loop.Tool{}, gate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(packet.Text, "Private topic") || gate.check(t.Context()) != nil {
		t.Fatal("forgotten question returned to the model")
	}
}

func forgetQuestionInput(t *testing.T, r *conversationRuntime, input ledger.Event) {
	t.Helper()
	remembered, err := coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Agent: r.agent, Person: r.person, Item: memory.Item{Kind: memory.Preference, Content: input.Body.Text(), Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: input.ID, Precision: memory.Exact}}}})
	if err != nil || !remembered.Remembered() {
		t.Fatalf("remember: %+v %v", remembered, err)
	}
	if err = r.removals.Forget(t.Context(), retract.Request{Person: r.person, Targets: []retract.Ref{{Kind: retract.Memory, ID: remembered.ID}}}); err != nil {
		t.Fatal(err)
	}
}
func TestQuestionForgottenCard(t *testing.T) {
	for _, action := range []protocol.QuestionAnswerAction{protocol.QuestionAnswerActionAccept, protocol.QuestionAnswerActionDecline, protocol.QuestionAnswerActionCancel} {
		t.Run(string(action), func(t *testing.T) {
			r := controlRuntime(t)
			input := questionPersonInput(t, r, "private", "Private preference")
			b, _ := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
			result := questionCall(t, b, "ask_question", "private", waiting.QuestionDraft{Question: "private-card-content", Recommendation: "default", Default: "default"})
			var q waiting.ConversationQuestion
			if result.Code != loop.OK || json.Unmarshal(result.Output, &q) != nil {
				t.Fatal(result)
			}
			forgetQuestionInput(t, r, input)
			card, err := (workerQuestions{r}).Question(t.Context(), r.person, q.ID)
			if err != nil || card.Card.Outcome == nil || *card.Card.Outcome != protocol.DecisionOutcomeWithdrawn || card.Form != nil {
				t.Errorf("forgotten card still actionable: %+v %v", card, err)
			}
			snapshot := workQuestionSnapshot(t, r)
			published := false
			for _, item := range snapshot.Items {
				var projected protocol.Question
				if item.Kind == protocol.KindQuestion && json.Unmarshal(item.Data, &projected) == nil && projected.Card != nil && projected.Card.ID == q.ID {
					published = projected.Card.Outcome != nil && *projected.Card.Outcome == protocol.DecisionOutcomeWithdrawn && projected.Form == nil
				}
			}
			if !published {
				t.Error("waiting snapshot did not withdraw the card")
			}
			names, err := r.ownerAgentNames(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			rows, err := r.ownerWaiting(t.Context(), names, waiting.Options{Now: time.Now()})
			if err != nil || rows.Total != 0 {
				t.Errorf("forgotten question remains actionable in waiting: %+v %v", rows, err)
			}
			version, value := "1", "chosen"
			values := protocol.FormValues{"answer": {String: &value}}
			outcome, err := (workerQuestions{r}).AnswerQuestionMessage(t.Context(), r.person, protocol.QuestionAnswer{CardID: &q.ID, Revision: &version, Answer: action, Values: &values}, "transport-id")
			if err != nil || outcome != protocol.DecisionOutcomeWithdrawn {
				t.Errorf("forgotten answer admitted: %s %v", outcome, err)
			}
			stored, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), q.ID)
			if err != nil || stored.Resolution != nil {
				t.Errorf("forgotten question resolved: %+v %v", stored, err)
			}
			var deliveries int
			if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='question-answer'").Scan(&deliveries)
			}); err != nil || deliveries != 0 {
				t.Errorf("forgotten answer queued: %d %v", deliveries, err)
			}
		})
	}
}
func TestQuestionAnswerForgetLineage(t *testing.T) {
	r := controlRuntime(t)
	input := questionPersonInput(t, r, "private", "Private preference")
	b, _ := questionBindings(t, r, input, agents.ProjectWorkMethod(agents.Preparation{}, agents.State{}))
	result := questionCall(t, b, "ask_question", "private", waiting.QuestionDraft{Question: "private-card-content", Recommendation: "default", Default: "default"})
	var q waiting.ConversationQuestion
	if result.Code != loop.OK || json.Unmarshal(result.Output, &q) != nil {
		t.Fatal(result)
	}
	version, value := "1", "chosen"
	values := protocol.FormValues{"answer": {String: &value}}
	if outcome, err := (workerQuestions{r}).AnswerQuestionMessage(t.Context(), r.person, protocol.QuestionAnswer{CardID: &q.ID, Revision: &version, Answer: protocol.QuestionAnswerActionAccept, Values: &values}, "transport-id"); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatal(outcome, err)
	}
	forgetQuestionInput(t, r, input)
	next := questionPersonInput(t, r, "next", "New topic")
	items, _, _, _, err := r.modelContext(t.Context(), next.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.Contains(string(item), "private-card-content") {
			t.Fatal("answer revived the forgotten question")
		}
	}
}
