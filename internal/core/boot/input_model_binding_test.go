// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"strings"
	"testing"
	"time"
)

// bindingArgs sends the canonical call shape; tests that cover other shapes
// build their arguments directly.
func bindingArgs(choices ...binding.Choice) map[string]any {
	return map[string]any{"bindings": choices}
}

func modelBindingTools(t *testing.T, r *conversationRuntime, input ledger.Event) map[string]loop.Tool {
	t.Helper()
	s := &turnState{runtime: r, input: input, turnID: "turn:" + input.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.bindings["bind_input"]; !ok {
		t.Fatal("running Core does not expose bind_input")
	}
	return s.bindings
}
func TestModelBindingTwoRequests(t *testing.T) {
	r, _ := turnRuntime(t)
	input := questionPersonInput(t, r, "two-requests", "Remember this. Start a new task.")
	tools := modelBindingTools(t, r, input)
	got := questionCall(t, tools, "bind_input", "split", map[string]any{"bindings": []any{
		map[string]any{"request": 0, "quote": "Remember this.", "target": map[string]string{"class": "none"}},
		map[string]any{"request": 1, "quote": "Start a new task.", "target": map[string]string{"class": "new", "kind": "todo"}},
	}})
	if got.Code != loop.OK {
		t.Fatalf("split: %+v", got)
	}
	var count int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM conv_event WHERE parent_id=? AND scaffold_kind='input_binding_choice'`, input.ID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("choice event count = %d", count)
	}
	var saved struct {
		Bindings []struct {
			Request int `json:"request"`
			Target  struct {
				Class string `json:"class"`
			} `json:"target"`
		} `json:"bindings"`
	}
	if json.Unmarshal(got.Output, &saved) != nil || len(saved.Bindings) != 2 || saved.Bindings[0].Target.Class != "none" || saved.Bindings[1].Target.Class != "new" {
		t.Fatalf("bindings: %s", got.Output)
	}
	if again := questionCall(t, tools, "bind_input", "split", map[string]any{"bindings": []any{
		map[string]any{"request": 0, "quote": "Remember this.", "target": map[string]string{"class": "none"}},
		map[string]any{"request": 1, "quote": "Start a new task.", "target": map[string]string{"class": "new", "kind": "todo"}},
	}}); again.Code != loop.OK {
		t.Fatal(again)
	}
}

func TestModelBindingPendingTarget(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := intakereplay.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "first-run", Title: "First run", Kind: "run", State: "running", LastActivity: now}, {ID: "second-run", Title: "Second run", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, intakereplay.Input{}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "ambiguous", "Change its output format.")
	tools := modelBindingTools(t, r, input)
	first := binding.Target{Class: "existing", ID: "first-run", Kind: "run"}
	second := binding.Target{Class: "existing", ID: "second-run", Kind: "run"}
	result := questionCall(t, tools, "bind_input", "choose", bindingArgs(binding.Choice{Request: 0, Quote: input.Body.Text(), Target: first, Tied: []binding.Target{first, second}, Action: "steer"}))
	if result.Code != loop.OK {
		saved, err := r.currentInputChoices(t.Context(), input)
		if err == nil && len(saved) > 0 && saved[0].Question != "" {
			err = r.questionDecisions().PublishQuestion(t.Context(), saved[0].Question)
		}
		t.Fatalf("tie: %+v saved=%+v err=%v", result, saved, err)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) != 1 || choices[0].Question == "" {
		t.Fatalf("not held: %+v %v", choices, err)
	}
	qid := choices[0].Question
	q, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), qid)
	if err != nil || len(q.Choices) != 2 || q.Recommendation != q.Choices[0] {
		t.Fatalf("question: %+v %v", q, err)
	}
	if got := questionCall(t, tools, "follow_work", "held", map[string]any{"id": "first-run", "text": "Change format"}); got.Code != loop.Denied {
		t.Fatalf("pending steer escaped: %+v", got)
	}
	unrelated := questionPersonInput(t, r, "unrelated", "What is a prime number?")
	other := modelBindingTools(t, r, unrelated)
	if got := questionCall(t, other, "bind_input", "unrelated", bindingArgs(binding.Choice{Quote: unrelated.Body.Text(), Target: binding.Target{Class: "none"}})); got.Code != loop.OK {
		t.Fatal(got)
	}
	if err = waiting.NewConversationQuestions(r.db).Resolve(t.Context(), r.person, r.agent, qid, waiting.QuestionResolution{Kind: "default_applied", Input: unrelated.ID, Value: q.Default, At: now.Add(24 * time.Hour)}); err == nil {
		t.Fatal("deadline/unrelated turn applied recommended target")
	}
	// Re-prepare from durable state, without relying on the prior tool closure.
	restored := modelBindingTools(t, r, input)
	if got := questionCall(t, restored, "follow_work", "still-held", map[string]any{"id": "second-run", "text": "Change format"}); got.Code != loop.Denied {
		t.Fatal(got)
	}
	card, err := r.conversationQuestionCard(t.Context(), r.person, qid)
	if err != nil || card.Card.Outcome != nil || len(card.Card.Targets) != 0 {
		t.Fatalf("card: %+v %v", card, err)
	}
	route := r.route
	r.route = nil
	defer func() { r.route = route }()
	revision, value := "1", q.Choices[1]
	values := protocol.FormValues{"answer": {String: &value}}
	answer := protocol.QuestionAnswer{CardID: &qid, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if outcome, err := r.answerConversationQuestion(t.Context(), r.person, answer, "answer"); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("answer: %s %v", outcome, err)
	}
	choices, err = r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Question != "" || choices[0].Target != second || choices[0].Decider != "person" {
		t.Fatalf("resolved: %+v %v", choices, err)
	}
	if outcome, err := r.answerConversationQuestion(t.Context(), r.person, answer, "duplicate"); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("duplicate: %s %v", outcome, err)
	}
}

func TestModelBindingDuplicateAndImplicitTarget(t *testing.T) {
	r, _ := turnRuntime(t)
	input := questionPersonInput(t, r, "create-todo", "Save a task")
	tools := modelBindingTools(t, r, input)
	args := map[string]any{"id": "", "revision": "", "kind": "todo", "title": "Garden", "note": "Plan the beds."}
	first := questionCall(t, tools, "write_record", "first", args)
	if first.Code != loop.OK {
		t.Fatalf("create: %+v", first)
	}
	var receipt struct{ ID string }
	_ = json.Unmarshal(first.Output, &receipt)
	input = questionPersonInput(t, r, "duplicate-todo", "Save another Garden task")
	tools = modelBindingTools(t, r, input)
	if duplicate := questionCall(t, tools, "write_record", "duplicate", args); duplicate.Code != loop.Invalid || !strings.Contains(string(duplicate.Output), "open_item_matches") {
		t.Fatalf("duplicate created: %+v", duplicate)
	}
	if got := questionCall(t, tools, "read_record", "existing", map[string]string{"id": receipt.ID}); got.Code != loop.OK {
		t.Fatal(got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Target.ID != receipt.ID || choices[0].Decider != "tool:read_record" {
		t.Fatalf("implicit target: %+v %v", choices, err)
	}
	if got := questionCall(t, tools, "bind_input", "distinct", bindingArgs(binding.Choice{Quote: input.Body.Text(), Target: binding.Target{Class: "new", Kind: "todo"}, Close: []string{receipt.ID}, Difference: "A different season and plot."})); got.Code != loop.OK {
		t.Fatal(got)
	}
	if got := questionCall(t, tools, "write_record", "second", args); got.Code != loop.OK {
		t.Fatal(got)
	}
	choices, err = r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Difference == "" {
		t.Fatalf("lost reason: %+v %v", choices, err)
	}
}

func TestModelBindingReplayUsesModelNotGold(t *testing.T) {
	r, p := turnRuntime(t)
	now := time.Now().UTC()
	c := intakereplay.Case{Version: 1, ID: "private-shape", Set: "hard", State: &binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}}, Questions: []binding.Question{}, Surfaces: map[string]string{}}, Input: binding.Input{Text: "Check it."}, Gold: []intakereplay.Gold{{Target: binding.Target{Class: "none"}}}}
	args := `{"bindings":[{"request":0,"quote":"Check it.","target":{"class":"existing","id":"garden","kind":"project"}}]}`
	call := provider.ToolCall{ID: "pick", Name: "bind_input", Arguments: args}
	raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": args})
	p.steps = []turnStep{{events: []provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.ToolCallReady, Call: &call}, {Kind: provider.Terminal}}}, {events: turnText("Bound.")}}
	got, err := ReplayIntakeModelCase(t.Context(), c, r.route)
	if err != nil || len(got) != 1 || got[0].Target.ID != "garden" || got[0].Decider != "model" {
		t.Fatalf("model replay: %+v %v", got, err)
	}
	if intakereplay.Score(c, got).Passed != 0 {
		t.Fatal("router copied the deliberately different gold label")
	}
	for _, request := range p.requests {
		for _, raw := range request.Items {
			if strings.Contains(string(raw), `\"gold\"`) || strings.Contains(string(raw), `\"observed\"`) {
				t.Fatal("label leaked into model request")
			}
		}
	}
}

type inputJudgeFunc func(context.Context, []atropos.ClosedChoice) ([]atropos.Judgment, error)

func (f inputJudgeFunc) Decide(ctx context.Context, q []atropos.ClosedChoice) ([]atropos.Judgment, error) {
	return f(ctx, q)
}
func TestModelBindingJudgeWiring(t *testing.T) {
	for _, confident := range []bool{true, false} {
		t.Run(fmt.Sprint(confident), func(t *testing.T) {
			r := controlRuntime(t)
			now := time.Now().UTC()
			state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "garden", Title: "Garden", Kind: "project", State: "active", LastActivity: now}}, Questions: []binding.Question{}, Surfaces: map[string]string{}}
			if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
				t.Fatal(err)
			}
			input := questionPersonInput(t, r, "judge", "Continue that.")
			calls := 0
			r.inputJudge = inputJudgeFunc(func(_ context.Context, questions []atropos.ClosedChoice) ([]atropos.Judgment, error) {
				calls++
				if len(questions) != 1 {
					t.Fatal(questions)
				}
				q := questions[0]
				probabilities := map[string]float64{}
				for _, c := range q.Candidates {
					probabilities[c.ID] = 0
				}
				probabilities["item:garden"] = .9
				probabilities["none"] = .1
				if !confident {
					probabilities["item:garden"] = .6
					probabilities["none"] = .4
				}
				return []atropos.Judgment{{ID: q.ID, Revision: q.Revision, Choice: "item:garden", Probabilities: probabilities}}, nil
			})
			if err := r.judgeInput(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			choices, err := r.currentInputChoices(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if (choices[0].Decider == "judge") != confident {
				t.Fatal(choices)
			}
			if confident && (choices[0].Target.ID != "garden" || choices[0].Probabilities["item:garden"] != .9) {
				t.Fatal(choices)
			}
			if err := r.judgeInput(t.Context(), input); err != nil || calls != 1 {
				t.Fatalf("judge retried: calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestModelBindingLargeInputSnapshot(t *testing.T) {
	r := controlRuntime(t)
	text := strings.Repeat("Synthetic long input. ", 900)
	input := questionPersonInput(t, r, "large-input", text)
	if _, err := r.saveModelBinding(t.Context(), input, "large-choice", []binding.Choice{{Quote: text, Target: binding.Target{Class: "none"}}}, "model"); err != nil {
		t.Fatal(err)
	}
	// Admission may read a previous large input's binding without opening its
	// externalized body under the canon writer.
	_ = questionPersonInput(t, r, "after-large", "Continue")
}

func TestModelBindingFollowSharedWorkScope(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "other-conversation-run", Title: "Shared work", Kind: "run", State: "waiting", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE background_runs SET record=json_set(record,'$.Origin.Conversation','another-conversation') WHERE id='other-conversation-run'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "shared-work", "Follow up on the shared work")
	bindings := modelBindingTools(t, r, input)
	if result := questionCall(t, bindings, "follow_work", "same-person", map[string]string{"id": "other-conversation-run", "text": "Keep the original scope."}); result.Code != loop.OK {
		t.Fatalf("person's shared queue was narrowed to this conversation: %+v", result)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE background_runs SET record=json_set(record,'$.Origin.Person','another-person') WHERE id='other-conversation-run'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if result := questionCall(t, bindings, "follow_work", "other-person", map[string]string{"id": "other-conversation-run", "text": "Must not deliver"}); result.Code != loop.Denied {
		t.Fatalf("foreign work accepted: %+v", result)
	}
}

func TestModelBindingReadDoesNotResolveRiskyTie(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "read-first", Title: "First analysis", Kind: "run", State: "running", LastActivity: now}, {ID: "read-second", Title: "Second analysis", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "read-then-steer", "Check its progress and change the format.")
	tools := modelBindingTools(t, r, input)
	a, b := binding.Target{Class: "existing", Kind: "run", ID: "read-first"}, binding.Target{Class: "existing", Kind: "run", ID: "read-second"}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingArgs(binding.Choice{Quote: input.Body.Text(), Target: a, Tied: []binding.Target{a, b}, Action: "read"})); got.Code != loop.OK {
		t.Fatal(got)
	}
	if got := questionCall(t, tools, "read_work_progress", "observe", map[string]string{"id": b.ID}); got.Code != loop.OK {
		t.Fatal(got)
	}
	if got := questionCall(t, tools, "follow_work", "must-hold", map[string]string{"id": b.ID, "text": "Change format"}); got.Code != loop.Denied {
		t.Fatalf("reading a candidate silently authorized steering: %+v", got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices[0].Tied) != 2 || choices[0].Question == "" {
		t.Fatalf("tie not held: %+v %v", choices, err)
	}
	run, err := r.background.service.Get(t.Context(), b.ID)
	if err != nil || len(run.FollowUps) != 0 {
		t.Fatalf("pending steer reached run: notes=%d err=%v", len(run.FollowUps), err)
	}
}

func TestModelBindingSteeringKeepsToolScope(t *testing.T) {
	r, _ := turnRuntime(t)
	input := questionPersonInput(t, r, "owner-turn", "Read available skills")
	steering := questionPersonInput(t, r, "scoped-steering", "Read available skills")
	restricted := newConversationView(r, r.agent, r.conversation)
	restricted.channelBinding = &channels.Binding{Scope: channels.Scope{Tools: false}}
	s := &turnState{runtime: r, input: input, turnID: "turn:" + input.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: restricted}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "skill_index", "scoped", map[string]any{"input_binding": toolBindingRef{Input: steering.ID, Request: 0}})
	if got.Code != loop.Denied {
		t.Fatalf("steering borrowed the original turn's tool scope: code=%s", got.Code)
	}
}
