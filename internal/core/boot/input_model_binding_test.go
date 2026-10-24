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
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
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

func TestDecodeBindingRequestIgnoresLaterPage(t *testing.T) {
	raw := `{"bindings":[{"request":0,"quote":"stop it","target":{"class":"none"},"more":[{"class":"existing","id":"wide-0","kind":"task"}]}]}`
	in, err := decodeBindingRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Model choices are normalized against state before storage. Core-only
	// paging metadata must not survive that boundary.
	choices, err := binding.NormalizeChoices(binding.State{}, "stop it", in.Bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || len(choices[0].More) != 0 || choices[0].Target.Class != "none" {
		t.Fatalf("model later page was kept: %+v", choices)
	}
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

func TestModelBindingWorkerInputFollowsPersonTarget(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "task-a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
		{ID: "task-b", Title: "Second task", Kind: "task", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	// Sessions belong to another person so the live owner view does not observe
	// these handles; only the binding gate reads them here.
	foreign := &conversationRuntime{person: "person:elsewhere"}
	sessions := map[string]string{"session-a": "task-a", "session-b": "task-b", "session-o": "turn:owner-start"}
	r.workers.mu.Lock()
	for id, task := range sessions {
		r.workers.sessions[id] = &workerSession{thread: worker.Thread{ID: id, TaskID: task}, origin: foreign}
	}
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		for id := range sessions {
			delete(r.workers.sessions, id)
		}
		r.workers.mu.Unlock()
	})
	input := questionPersonInput(t, r, "worker-steer-target", "Change the format.")
	tools := modelBindingTools(t, r, input)
	b := binding.Target{Class: "existing", ID: "task-b", Kind: "task"}
	if _, err := r.saveModelBinding(t.Context(), input, "worker-person-choice", []binding.Choice{{Quote: input.Body.Text(), Target: b}}, "person"); err != nil {
		t.Fatal(err)
	}
	call := func(session string) *loop.Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"session": session, "turn": "turn-1", "generation": 1, "binding": 1, "text": "Change the format."})
		return r.bindWorkCall(t.Context(), input, 0, "turn:"+input.ID, provider.ToolCall{ID: "steer-" + session, Name: "worker_input", Arguments: string(raw)})
	}
	if result := call("session-a"); result == nil || result.Code != loop.Denied {
		t.Fatalf("a worker for another task accepted the person's request: %+v", result)
	}
	if result := call("session-b"); result != nil {
		t.Fatalf("the person's chosen task was refused: %+v", *result)
	}
	// An owner-started session names a conversation turn, not a work item, so it
	// stays open to the original tool's own checks.
	if result := call("session-o"); result != nil {
		t.Fatalf("an owner-started session was refused: %+v", *result)
	}
	if got := questionCall(t, tools, "worker_input", "wrapped-steer", map[string]any{"session": "session-a", "turn": "turn-1", "generation": 1, "binding": 1, "text": "Change the format."}); got.Code != loop.Denied {
		t.Fatalf("the tool wrapper delivered a steer to another task: %+v", got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) != 1 || choices[0].Target != b || choices[0].Decider != "person" {
		t.Fatalf("the person's choice changed: %+v %v", choices, err)
	}
}

func TestModelBindingRecoveredSteeringTargets(t *testing.T) {
	for _, name := range []string{"worker_input", "worker_interrupt", "follow_work_session", "follow_work_task"} {
		t.Run(name, func(t *testing.T) {
			r, _ := turnRuntime(t)
			now := time.Now().UTC()
			state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
				{ID: "task:a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
				{ID: "task:b", Title: "Second task", Kind: "task", State: "running", LastActivity: now},
			}, Surfaces: map[string]string{}}
			if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
				t.Fatal(err)
			}
			input := questionPersonInput(t, r, "recovered-steer", "Change the format.")
			chosen := binding.Target{Class: "existing", ID: "task:b", Kind: "task"}
			if _, err := r.saveModelBinding(t.Context(), input, "person-choice", []binding.Choice{{Quote: input.Body.Text(), Target: chosen}}, "person"); err != nil {
				t.Fatal(err)
			}
			// Only durable recovery records survive the restart; neither session
			// is present in the live worker registry.
			for _, suffix := range []string{"a", "b"} {
				id := "session-" + suffix
				if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), recovery.Record{
					ID: id, Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: input.ID,
					Thread: worker.Thread{ID: id, TaskID: "task:" + suffix},
				}); err != nil {
					t.Fatal(err)
				}
			}
			for _, suffix := range []string{"a", "b"} {
				toolName := name
				args := map[string]any{"session": "session-" + suffix, "text": "Change the format."}
				if strings.HasPrefix(name, "follow_work") {
					toolName = "follow_work"
					args = map[string]any{"id": "session:session-" + suffix, "text": "Change the format."}
					if name == "follow_work_task" {
						args["id"] = "task:task:" + suffix + ":1"
					}
				}
				raw, _ := json.Marshal(args)
				got := r.bindWorkCall(t.Context(), input, 0, "turn:"+input.ID, provider.ToolCall{ID: suffix, Name: toolName, Arguments: string(raw)})
				if suffix == "a" && (got == nil || got.Code != loop.Denied) {
					t.Errorf("another task passed the binding gate: %+v", got)
				}
				if suffix == "b" && got != nil {
					t.Errorf("chosen task refused: %+v", got)
				}
			}
			choices, err := r.currentInputChoices(t.Context(), input)
			if err != nil || len(choices) != 1 || choices[0].Target != chosen || choices[0].Decider != "person" {
				t.Fatalf("person's choice changed: %+v %v", choices, err)
			}
		})
	}
}

func TestModelBindingReplyUsesEffectiveTarget(t *testing.T) {
	for _, creation := range []string{"model", "prepare_work", "schedule_work"} {
		t.Run(creation, func(t *testing.T) {
			r, _ := turnRuntime(t)
			now := time.Now().UTC()
			state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
				{ID: "task-a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
				{ID: "task-b", Title: "Second task", Kind: "task", State: "running", LastActivity: now},
			}, Surfaces: map[string]string{}}
			if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
				t.Fatal(err)
			}
			input := questionPersonInput(t, r, "original", "First task")
			target := binding.Target{Class: "existing", ID: "task-b", Kind: "task"}
			if creation != "model" {
				target = binding.Target{Class: "new", Kind: "task"}
			}
			if _, err := r.saveModelBinding(t.Context(), input, "binding-tool:turn:create", []binding.Choice{{Quote: input.Body.Text(), Target: target}}, "model"); err != nil {
				t.Fatal(err)
			}
			if creation != "model" {
				output, _ := json.Marshal(work.Workspace{TaskID: "task-b"})
				result := loop.Result{Code: loop.OK, Output: output}
				if creation == "schedule_work" {
					result = delegationResult("create", []string{"task-b"}, nil)
				}
				if err := r.recordCreatedBinding(t.Context(), input, 0, "turn", provider.ToolCall{ID: "create", Name: creation}, result); err != nil {
					t.Fatal(err)
				}
				choices, err := r.currentInputChoices(t.Context(), input)
				if err != nil || len(choices) != 1 || choices[0].CreatedID != "task-b" {
					t.Fatalf("creation identity lost: %+v %v", choices, err)
				}
			}
			for _, surface := range []ledger.Surface{{Kind: "tui", ReplyTo: input.ID}, {Kind: "tui", Thread: input.ID}} {
				id := "reply"
				if surface.Thread != "" {
					id = "thread"
				}
				reply, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Continue"})
				if err != nil {
					t.Fatal(err)
				}
				choices, err := r.currentInputChoices(t.Context(), reply)
				if err != nil || len(choices) != 1 || choices[0].Target.ID != "task-b" || choices[0].Decider != "reply-or-card" {
					t.Fatalf("%s used superseded admission target: %+v %v", id, choices, err)
				}
			}
		})
	}
}

func TestModelBindingFollowRawTaskGeneration(t *testing.T) {
	r, _ := turnRuntime(t)
	dir := t.TempDir()
	reserve := t.TempDir()
	task := work.Task{ID: "followed-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Followed task", Done: "Done", ArtifactRoots: []string{dir}}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "followed-run", TaskID: task.ID, Generation: 1, Worker: "codex", ThreadID: "followed-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "followed-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "followed-thread", TaskID: task.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "followed-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	input := questionPersonInput(t, r, "follow", "Continue the current task.")
	bindings := modelBindingTools(t, r, input)
	if got := questionCall(t, bindings, "follow_work", "current", map[string]string{"id": task.ID, "text": "Change the format."}); got.Code != loop.OK {
		t.Fatalf("current generation refused: %+v", got)
	}
	if probe.inputs != 1 || !strings.Contains(probe.text, "Change the format.") || strings.Contains(probe.text, `speaker_kind="person"`) {
		t.Fatalf("agent follow-up missing: inputs=%d text=%s", probe.inputs, probe.text)
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

func TestSelectedNewTargetResolvesToCreatedIdentity(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "spawned-run", Title: "Spawned run", Kind: "run", State: "running", LastActivity: now},
		{ID: "other-run", Title: "Other run", Kind: "run", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "new-created", "Start one and then check it.")
	tools := modelBindingTools(t, r, input)
	fresh := binding.Target{Class: "new", Kind: "run"}
	other := binding.Target{Class: "existing", ID: "other-run", Kind: "run"}
	if got := questionCall(t, tools, "bind_input", "ask", bindingRequest{Bindings: []binding.Choice{{
		Quote: input.Body.Text(), Target: fresh, Tied: []binding.Target{fresh, other}, Action: "start",
	}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Question == "" {
		t.Fatalf("question: %+v %v", choices, err)
	}
	q, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), choices[0].Question)
	if err != nil {
		t.Fatal(err)
	}
	route := r.route
	r.route = nil
	defer func() { r.route = route }()
	revision, value := "1", q.Choices[0]
	values := protocol.FormValues{"answer": {String: &value}}
	answer := protocol.QuestionAnswer{CardID: &q.ID, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if outcome, err := r.answerConversationQuestion(t.Context(), r.person, answer, "answer"); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("answer: %s %v", outcome, err)
	}
	turnID := "turn:" + input.ID
	if _, err = r.saveModelBinding(t.Context(), input, "binding-tool:"+turnID+":create", []binding.Choice{{
		Quote: input.Body.Text(), Target: fresh,
	}}, "tool:start_run"); err != nil {
		t.Fatal(err)
	}
	if err = r.recordCreatedBinding(t.Context(), input, 0, turnID, provider.ToolCall{ID: "create", Name: "start_run"}, loop.Result{Code: loop.OK, Output: json.RawMessage(`{"id":"spawned-run"}`)}); err != nil {
		t.Fatal(err)
	}
	choices, err = r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Decider != "person" || choices[0].Target != fresh || choices[0].CreatedID != "spawned-run" {
		t.Fatalf("created choice: %+v %v", choices, err)
	}
	if got := questionCall(t, tools, "read_work_progress", "other-progress", map[string]string{"id": other.ID}); got.Code != loop.Denied || !strings.Contains(string(got.Output), "input_target_pending") {
		t.Fatalf("other item was accepted: %+v", got)
	}
	if got := questionCall(t, tools, "read_record", "created-record", map[string]string{"id": "spawned-run"}); strings.Contains(string(got.Output), "input_target_pending") {
		t.Fatalf("record read of the created identity stayed pending: %+v", got)
	}
	if got := questionCall(t, tools, "read_work_progress", "created-progress", map[string]string{"id": "spawned-run"}); got.Code != loop.OK {
		t.Fatalf("progress read of the created identity stayed pending: %+v", got)
	}
	if got := questionCall(t, tools, "follow_work", "created-follow", map[string]string{"id": "spawned-run", "text": "Check the result"}); got.Code != loop.OK {
		t.Fatalf("follow-up on the created identity: %+v", got)
	}
	if got := questionCall(t, tools, "follow_work", "other-follow", map[string]string{"id": other.ID, "text": "Change the other run"}); got.Code != loop.Denied || !strings.Contains(string(got.Output), "input_target_pending") {
		t.Fatalf("other follow-up was accepted: %+v", got)
	}
	choices, err = r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Decider != "person" || choices[0].CreatedID != "spawned-run" || choices[0].Target.ID != "spawned-run" {
		t.Fatalf("creation record dropped: %+v %v", choices, err)
	}
	run, err := r.background.service.Get(t.Context(), "spawned-run")
	if err != nil || len(run.FollowUps) != 1 {
		t.Fatalf("follow-up missing: notes=%d err=%v", len(run.FollowUps), err)
	}
}

// A task may be named like a generation reference. The gate and execution share
// one resolver, so the follow-up reaches the task the person selected, and an
// unmatched suffix still falls back to the task it qualifies.
func TestModelBindingFollowPrefersExactTaskID(t *testing.T) {
	r, _ := turnRuntime(t)
	probes := map[string]*messageProbe{}
	for _, id := range []string{"a", "a:1", "b"} {
		dir := t.TempDir()
		task := work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Task " + id, Done: "Done", ArtifactRoots: []string{dir}}
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if err := r.work.Start(t.Context(), work.Run{ID: "run-" + id, TaskID: id, Generation: 1, Worker: "codex", ThreadID: "session-" + id, TurnID: "old", NodeID: "node"}); err != nil {
			t.Fatal(err)
		}
		reserve := t.TempDir()
		if err := r.work.ReserveWorker(t.Context(), "worker-"+id, reserve); err != nil {
			t.Fatal(err)
		}
		thread := worker.Thread{ID: "session-" + id, TaskID: id, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
		probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
		done := make(chan struct{})
		close(done)
		entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "worker-" + id, process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
		r.workers.mu.Lock()
		r.workers.sessions[thread.ID] = entry
		r.workers.mu.Unlock()
		probes[id] = probe
	}
	t.Cleanup(func() {
		r.workers.mu.Lock()
		for id := range probes {
			delete(r.workers.sessions, "session-"+id)
		}
		r.workers.mu.Unlock()
	})
	input := questionPersonInput(t, r, "exact-task-id", "Change the format.")
	tools := modelBindingTools(t, r, input)
	if _, err := r.saveModelBinding(t.Context(), input, "exact-task-person-choice", []binding.Choice{{Quote: input.Body.Text(), Target: binding.Target{Class: "existing", ID: "a:1", Kind: "task"}}}, "person"); err != nil {
		t.Fatal(err)
	}
	if got := questionCall(t, tools, "follow_work", "exact", map[string]string{"id": "a:1", "text": "Change the format."}); got.Code != loop.OK {
		t.Fatalf("the selected task was refused: %+v", got)
	}
	if probes["a:1"].inputs != 1 || probes["a"].inputs != 0 {
		t.Fatalf("follow-up reached a=%d a:1=%d", probes["a"].inputs, probes["a:1"].inputs)
	}
	// Without a settled choice the suffix still resolves to the task it qualifies.
	unsettled := questionPersonInput(t, r, "suffix-task-id", "Change the format.")
	other := modelBindingTools(t, r, unsettled)
	if got := questionCall(t, other, "follow_work", "suffix", map[string]string{"id": "b:7", "text": "Change the format."}); got.Code != loop.OK {
		t.Fatalf("suffix reference refused: %+v", got)
	}
	if probes["b"].inputs != 1 {
		t.Fatalf("suffix reference reached b=%d", probes["b"].inputs)
	}
}

func TestReadWorkProgressKeepsAcknowledgedCompletion(t *testing.T) {
	r, _ := turnRuntime(t)
	card := work.Card{Task: work.Task{ID: "finished-task", AgentID: r.agent, PersonID: r.person, ConversationID: r.conversation, Title: "Finished materials"}, State: "done", Verdict: "verified", Run: &work.Run{Generation: 1}}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", card.Task.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "progress-ask", "Where did the materials land?")
	tools := modelBindingTools(t, r, input)
	read := func() (string, string) {
		t.Helper()
		got := questionCall(t, tools, "read_work_progress", "observe-"+card.Task.ID, map[string]string{"id": card.Task.ID})
		if got.Code != loop.OK {
			t.Fatalf("progress read: %+v", got)
		}
		var body struct {
			Progress struct {
				State *string `json:"state"`
				Name  string  `json:"name"`
			} `json:"progress"`
		}
		if json.Unmarshal(got.Output, &body) != nil {
			t.Fatalf("progress body: %s", got.Output)
		}
		state := ""
		if body.Progress.State != nil {
			state = *body.Progress.State
		}
		return state, body.Progress.Name
	}
	if state, name := read(); state != "done" || name != card.Task.Title {
		t.Fatalf("unseen completion = %s %q", state, name)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO running_seen(source,item_id,person) VALUES('work',?,?)", card.Task.ID+":1", r.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rows != nil {
		for _, row := range *snapshot.Rows {
			if row.Ref == card.Task.ID {
				t.Fatalf("acknowledged completion stayed on the running list: %+v", row)
			}
		}
	}
	if state, name := read(); state != "done" || name != card.Task.Title {
		t.Fatalf("acknowledged completion lost: state=%s name=%q", state, name)
	}
	missing := questionCall(t, tools, "read_work_progress", "missing", map[string]string{"id": "missing-task"})
	if missing.Code != loop.OK || !strings.Contains(string(missing.Output), `"state":"unknown"`) {
		t.Fatalf("unobserved progress: %+v", missing)
	}
}

func TestFollowWorkRecoversChannelReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	const runID = "channel-run"
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: runID, Title: "Shared draft", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	channel := questionChannelInputs(t, r)
	view, input := channel("one", "first")
	follow := func(turnID string, event ledger.Event, text string) loop.Result {
		t.Helper()
		bindings := map[string]loop.Tool{}
		if _, err := view.prepareBindingWorkTools(turnID, &toolPreparation{event: event}, bindings); err != nil {
			t.Fatal(err)
		}
		return questionCall(t, bindings, "follow_work", turnID, map[string]string{"id": runID, "text": text})
	}
	if got := follow("direct", input, "Keep the original scope."); got.Code != loop.OK {
		t.Fatalf("channel input follow-up: %+v", got)
	}
	at := time.Now().UTC()
	answer, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "target-question:answer", ParentID: input.ID, ConversationID: r.conversation, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "question_answer", OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Recorded your response: Shared draft"})
	if err != nil {
		t.Fatal(err)
	}
	if got := follow("answer", answer, "Use a table."); got.Code != loop.OK {
		t.Fatalf("answered target did not resume the channel follow-up: %+v", got)
	}
	outer, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "later-answer", ParentID: answer.ID, ConversationID: r.conversation, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "question_answer", OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Recorded a later response"})
	if err != nil {
		t.Fatal(err)
	}
	if got := follow("chain", outer, "Keep the table."); got.Code != loop.OK {
		t.Fatalf("continuation chain lost the channel receipt: %+v", got)
	}
	record, err := r.background.service.Get(t.Context(), runID)
	if err != nil || len(record.FollowUps) != 3 || record.FollowUps[1] != "Use a table." {
		t.Fatalf("follow-ups: %+v %v", record.FollowUps, err)
	}
}

func TestFollowWorkKeepsChannelSteeringReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	const runID = "steered-run"
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: runID, Title: "Shared draft", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	app := questionPersonInput(t, r, "app-turn", "Keep going in the app")
	channel := questionChannelInputs(t, r)
	view, steering := channel("one", "first")
	s := &turnState{runtime: r, input: app, turnID: "turn:" + app.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "follow_work", "from-app", map[string]any{"id": runID, "text": "Keep the channel scope.", "input_binding": map[string]any{"input_event_id": steering.ID}})
	if got.Code != loop.OK {
		t.Fatalf("app turn did not accept the channel follow-up: %+v", got)
	}
	record, err := r.background.service.Get(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.FollowSources) != 1 {
		t.Fatalf("channel steering follow-up lost its receipt: sources=%d ups=%v", len(record.FollowSources), record.FollowUps)
	}
	var source channelFollowSource
	if json.Unmarshal(record.FollowSources[0], &source) != nil || source.Sender != "42" || source.Address != view.channelAddress || !source.Addressed {
		t.Fatalf("receipt: %s", record.FollowSources[0])
	}
}
