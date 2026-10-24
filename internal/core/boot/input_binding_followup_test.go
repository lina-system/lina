// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	goaleng "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func insertWorkCard(t *testing.T, r *conversationRuntime, task work.Task) {
	t.Helper()
	raw, err := json.Marshal(work.Card{Task: task, State: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", task.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyPreparedTaskStaysInPersonScope(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now().UTC()
	insertWorkCard(t, r, work.Task{ID: "legacy-task", AgentID: r.agent, ConversationID: r.conversation, Title: "Legacy draft", Done: "Draft exists"})
	insertWorkCard(t, r, work.Task{ID: "other-agent-task", AgentID: "other-agent", ConversationID: r.conversation, Title: "Foreign agent", Done: "Hidden"})
	insertWorkCard(t, r, work.Task{ID: "other-person-task", PersonID: "someone-else", AgentID: r.agent, ConversationID: r.conversation, Title: "Foreign person", Done: "Hidden"})
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "mention-legacy", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed",
		Body: "Please check legacy-task other-agent-task and other-person-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(t.Context(), inputBindingID(input.ID))
	if err != nil {
		t.Fatal(err)
	}
	var decision binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Target.ID != "legacy-task" || decision.Decider != "exact-reference" {
		t.Fatalf("legacy task excluded: %+v", decision)
	}
}

func TestReplyFollowsSingleCreatedTarget(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "project-a", Title: "Alpha Project", Kind: "project", State: "active", LastActivity: now},
		{ID: "run-b", Title: "Spawned run", Kind: "run", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "created-parent", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Alpha Project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		return appendInputChoices(t.Context(), tx, input, "binding-created:one", []binding.Choice{{
			Request: 0, Quote: input.Body.Text(), Target: binding.Target{Class: "new", Kind: "run"}, CreatedID: "run-b",
		}})
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "created-reply", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui", ReplyTo: input.ID}, OccurredAt: now.Add(time.Second), RecordedAt: now.Add(time.Second), TimeQuality: "observed", Body: "thanks",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(t.Context(), inputBindingID(reply.ID))
	if err != nil {
		t.Fatal(err)
	}
	var decision binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Target.ID != "run-b" || decision.Decider != "reply-or-card" {
		t.Fatalf("reply kept the admission target: %+v", decision)
	}
}

func TestReplyLeavesMultipleTargetsUnresolved(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "project-a", Title: "Alpha Project", Kind: "project", State: "active", LastActivity: now},
		{ID: "run-b", Title: "Spawned run", Kind: "run", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "split-parent", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Alpha Project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		return appendInputChoices(t.Context(), tx, input, "binding-created:split", []binding.Choice{
			{Request: 0, Quote: "Alpha", Target: binding.Target{Class: "existing", ID: "project-a", Kind: "project"}},
			{Request: 1, Quote: "Project", Target: binding.Target{Class: "existing", ID: "run-b", Kind: "run"}},
		})
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "split-reply", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui", ReplyTo: input.ID}, OccurredAt: now.Add(time.Second), RecordedAt: now.Add(time.Second), TimeQuality: "observed", Body: "thanks",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(t.Context(), inputBindingID(reply.ID))
	if err != nil {
		t.Fatal(err)
	}
	var decision binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Rule == "reply-or-card" {
		t.Fatalf("several targets still settled the reply: %+v", decision)
	}
}

func TestCreatedReceiptBecomesNextFocus(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now().UTC()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "prepare-focus", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Prepare the draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.saveModelBinding(t.Context(), input, "binding-tool:turn:prep", []binding.Choice{{Quote: input.Body.Text(), Target: binding.Target{Class: "new", Kind: "task"}}}, "tool:prepare_work"); err != nil {
		t.Fatal(err)
	}
	if err = r.recordCreatedBinding(t.Context(), input, 0, "turn", provider.ToolCall{ID: "prep", Name: "prepare_work"}, loop.Result{Code: loop.OK, Output: json.RawMessage(`{"task_id":"prepared-1","path":"/work","spec":{}}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.saveModelBinding(t.Context(), input, "binding-tool:turn:sched", []binding.Choice{{Request: 1, Quote: "draft", Target: binding.Target{Class: "new", Kind: "task"}}}, "tool:schedule_work"); err != nil {
		t.Fatal(err)
	}
	if err = r.recordCreatedBinding(t.Context(), input, 1, "turn", provider.ToolCall{ID: "sched", Name: "schedule_work"}, loop.Result{Code: loop.OK, Output: json.RawMessage(`{"result":["scheduled-1"]}`)}); err != nil {
		t.Fatal(err)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) < 2 || choices[0].CreatedID != "prepared-1" || choices[1].CreatedID != "scheduled-1" {
		t.Fatalf("single receipts were not recorded: %+v", choices)
	}
	if err = r.recordCreatedBinding(t.Context(), input, 1, "turn", provider.ToolCall{ID: "sched", Name: "schedule_work"}, loop.Result{Code: loop.OK, Output: json.RawMessage(`{"result":["one","two"]}`)}); err != nil {
		t.Fatal(err)
	}
	choices, err = r.currentInputChoices(t.Context(), input)
	if err != nil || choices[1].CreatedID != "scheduled-1" {
		t.Fatalf("several tasks became one focus: %+v %v", choices, err)
	}
	insertWorkCard(t, r, work.Task{ID: "scheduled-1", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Scheduled", Done: "Done"})
	next, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "after-create", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now.Add(time.Second), RecordedAt: now.Add(time.Second), TimeQuality: "observed", Body: "continue",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := r.journal().Get(t.Context(), inputBindingID(next.ID))
	if err != nil {
		t.Fatal(err)
	}
	var decision binding.Decision
	if err = json.Unmarshal([]byte(saved.Body.Text()), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Target.ID != "scheduled-1" {
		t.Fatalf("next input missed the created task: %+v", decision)
	}
}

func TestNewTargetQuestionLabelsStayDistinct(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "proj-a", Title: "Alpha", Kind: "project", State: "active", LastActivity: now},
		{ID: "proj-b", Title: "Beta", Kind: "project", State: "active", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "new-tie", "Start a run under one of the projects.")
	tools := modelBindingTools(t, r, input)
	first := binding.Target{Class: "new", Kind: "run", Parent: "proj-a"}
	second := binding.Target{Class: "new", Kind: "run", Parent: "proj-b"}
	got := questionCall(t, tools, "bind_input", "ask", bindingRequest{Bindings: []binding.Choice{{
		Quote: input.Body.Text(), Target: first, Tied: []binding.Target{first, second}, Action: "start",
	}}})
	if got.Code != loop.OK {
		t.Fatalf("new-target question was not saved: %+v", got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Question == "" {
		t.Fatalf("question missing: %+v %v", choices, err)
	}
	q, err := waiting.NewConversationQuestions(r.db).Get(t.Context(), choices[0].Question)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Choices) != 2 || q.Choices[0] == q.Choices[1] || !strings.Contains(q.Choices[0], "run") || !strings.Contains(q.Choices[0], "proj-a") || !strings.Contains(q.Choices[1], "proj-b") {
		t.Fatalf("labels: %+v", q.Choices)
	}
}

func TestFollowWorkKeepsAgentBudget(t *testing.T) {
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
	input := questionPersonInput(t, r, "follow-budget", "Check the followed task.")
	tools := modelBindingTools(t, r, input)
	if _, err := r.saveModelBinding(t.Context(), input, "binding-model:follow-budget", []binding.Choice{{Quote: input.Body.Text(), Target: binding.Target{Class: "existing", ID: task.ID, Kind: "task"}}}, "model"); err != nil {
		t.Fatal(err)
	}
	for i := range 13 {
		got := questionCall(t, tools, "follow_work", fmt.Sprintf("follow-%d", i), map[string]string{"id": "task:" + task.ID + ":1", "text": "Continue the task"})
		if i < 12 && got.Code != loop.OK {
			t.Fatalf("follow %d: %+v", i, got)
		}
		if i == 12 && got.Code == loop.OK {
			t.Fatal("another follow-up replenished the coordination allowance")
		}
	}
	if strings.Contains(probe.text, `speaker_kind="person"`) {
		t.Fatalf("model follow-up was stored as the person: %s", probe.text)
	}
}

func TestFollowWorkNormalizesChosenWorker(t *testing.T) {
	r, _ := turnRuntime(t)
	dir := t.TempDir()
	reserve := t.TempDir()
	chosen := work.Task{ID: "chosen-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Chosen task", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	other := work.Task{ID: "other-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Other task", Done: "Done", ArtifactRoots: []string{dir}}
	for _, task := range []work.Task{chosen, other} {
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "other-run", TaskID: other.ID, Generation: 1, Worker: "codex", ThreadID: "other-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "other-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "other-thread", TaskID: other.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "other-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	input := questionPersonInput(t, r, "chosen-worker", "Stay with the chosen task.")
	tools := modelBindingTools(t, r, input)
	if _, err := r.saveModelBinding(t.Context(), input, "binding-person:chosen-worker", []binding.Choice{{Quote: input.Body.Text(), Target: binding.Target{Class: "existing", ID: chosen.ID, Kind: "task"}}}, "person"); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, tools, "follow_work", "other-spelling", map[string]string{"id": "task:" + other.ID + ":1", "text": "Steer the other task"})
	if got.Code != loop.Denied || !strings.Contains(string(got.Output), "input_target_pending") {
		t.Fatalf("worker spelling bypassed the chosen task: %+v", got)
	}
	if probe.inputs != 0 {
		t.Fatalf("other task received %d follow-ups", probe.inputs)
	}
	direct := questionCall(t, tools, "worker_input", "direct-other", map[string]any{"session": thread.ID, "turn": "old", "generation": 1, "binding": 1, "text": "Steer the other task"})
	if direct.Code != loop.Denied || !strings.Contains(string(direct.Output), "input_target_pending") {
		t.Fatalf("worker_input bypassed the chosen task: %+v", direct)
	}
	if probe.inputs != 0 {
		t.Fatalf("direct worker input reached the other task %d times", probe.inputs)
	}
}

func TestFollowWorkUsesSelectedChannelSource(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{{ID: "channel-run", Title: "Channel run", Kind: "run", State: "running", LastActivity: now}}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	channel := questionChannelInputs(t, r)
	view, steering := channel("follow-source", "follow-native")
	tui := questionPersonInput(t, r, "tui-turn", "Continue in the app")
	if _, err := view.saveModelBinding(t.Context(), steering, "binding-model:channel-follow", []binding.Choice{{Quote: steering.Body.Text(), Target: binding.Target{Class: "existing", ID: "channel-run", Kind: "run"}}}, "model"); err != nil {
		t.Fatal(err)
	}
	s := &turnState{runtime: r, input: tui, turnID: "turn:" + tui.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "follow_work", "channel-follow", map[string]any{"id": "channel-run", "text": "Check the channel run", "input_binding": map[string]any{"input_event_id": steering.ID}})
	if got.Code != loop.OK {
		t.Fatalf("selected channel follow: %+v", got)
	}
	run, err := view.background.service.Get(t.Context(), "channel-run")
	if err != nil || len(run.FollowSources) != 1 {
		t.Fatalf("channel source missing: sources=%d err=%v", len(run.FollowSources), err)
	}
}

func TestBindInputAcceptsMessageSelectorAndExtraTargetFields(t *testing.T) {
	r, _ := turnRuntime(t)
	text := "Start a fresh checklist"
	input := questionPersonInput(t, r, "usable-args", text)
	tools := modelBindingTools(t, r, input)
	spaced := questionCall(t, tools, "bind_input", "spaced", map[string]any{
		"input_event_id": "  start a fresh   checklist ",
		"bindings": []any{map[string]any{
			"request": 0, "quote": text,
			"target": map[string]any{"class": "new", "kind": "todo", "title": "ignored metadata"},
		}},
	})
	if spaced.Code != loop.OK {
		t.Fatalf("message text in input_event_id rejected the binding: %+v", spaced)
	}
	prose := questionCall(t, tools, "bind_input", "prose", map[string]any{
		"input_event_id": "the person's message, not an event id",
		"bindings": []any{map[string]any{
			"request": 0, "quote": text, "target": map[string]string{"class": "none"},
		}},
	})
	if prose.Code != loop.OK {
		t.Fatalf("prose selector rejected the only input: %+v", prose)
	}
	unknown := questionCall(t, tools, "bind_input", "unknown-id", map[string]any{
		"input_event_id": "evt-not-admitted",
		"bindings": []any{map[string]any{
			"request": 0, "quote": text, "target": map[string]string{"class": "none"},
		}},
	})
	if unknown.Code == loop.OK {
		t.Fatal("an unknown event id was accepted")
	}
}

func TestResumedChannelFollowRecoversReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "chan-first", Title: "First channel run", Kind: "run", State: "running", LastActivity: now},
		{ID: "chan-second", Title: "Second channel run", Kind: "run", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	channel := questionChannelInputs(t, r)
	view, input := channel("held", "held-native")
	tools := modelBindingTools(t, view, input)
	first := binding.Target{Class: "existing", ID: "chan-first", Kind: "run"}
	second := binding.Target{Class: "existing", ID: "chan-second", Kind: "run"}
	if got := questionCall(t, tools, "bind_input", "tie", bindingRequest{Bindings: []binding.Choice{{
		Quote: input.Body.Text(), Target: first, Tied: []binding.Target{first, second}, Action: "steer",
	}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	choices, err := view.currentInputChoices(t.Context(), input)
	if err != nil || choices[0].Question == "" {
		t.Fatalf("not held: %+v %v", choices, err)
	}
	q, err := waiting.NewConversationQuestions(view.db).Get(t.Context(), choices[0].Question)
	if err != nil {
		t.Fatal(err)
	}
	view.route = nil
	revision, value := "1", q.Choices[1]
	values := protocol.FormValues{"answer": {String: &value}}
	answer := protocol.QuestionAnswer{CardID: &q.ID, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if outcome, err := view.answerConversationQuestion(t.Context(), view.person, answer, "channel-answer"); err != nil || outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("answer: %s %v", outcome, err)
	}
	resumed, err := view.journal().Get(t.Context(), q.ID+":answer")
	if err != nil {
		t.Fatal(err)
	}
	s := &turnState{runtime: view, input: resumed, turnID: "turn:" + resumed.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}}
	if _, err = view.prepareBindingWorkTools(s.turnID, &toolPreparation{event: resumed}, s.bindings); err != nil {
		t.Fatal(err)
	}
	s.prepareInputBinding(nil)
	if _, ok := s.bindings["follow_work"]; !ok {
		t.Fatal("resumed turn dropped follow_work")
	}
	if got := questionCall(t, s.bindings, "follow_work", "resume", map[string]string{"id": second.ID, "text": "Change format"}); got.Code != loop.OK {
		t.Fatalf("resumed channel follow denied: %+v", got)
	}
	run, err := view.background.service.Get(t.Context(), second.ID)
	if err != nil || len(run.FollowUps) != 1 {
		t.Fatalf("follow-up missing: notes=%d err=%v", len(run.FollowUps), err)
	}
}

func TestReviseCodeGoalChecksChosenRecord(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	input := questionPersonInput(t, r, "goal-choice", "Correct the chosen project.")
	tools := modelBindingTools(t, r, input)
	admit := func(id, outcome string) (goalID, recordID, revision string) {
		t.Helper()
		got := questionCall(t, tools, "dispatch_code_goal", id, map[string]any{
			"outcome": outcome, "done": "checks pass",
			"scope":             map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
			"criteria":          []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
			"no_service_reason": "library",
			"tasks":             []any{},
		})
		if got.Code != loop.OK {
			t.Fatalf("admit %s: %+v", id, got)
		}
		var fields map[string]string
		if err := json.Unmarshal(got.Output, &fields); err != nil {
			t.Fatal(err)
		}
		return fields["goal_id"], fields["record_id"], fields["revision"]
	}
	chosenID, chosenRecord, chosenRevision := admit("goal-a", "chosen outcome")
	otherID, _, otherRevision := admit("goal-b", "other outcome")
	wantRecord, ok := goaleng.ProjectRecordID(chosenID)
	if !ok || chosenRecord != wantRecord {
		t.Fatalf("record id = %s", chosenRecord)
	}
	if _, err := r.saveModelBinding(t.Context(), input, "binding-person:goal-choice", []binding.Choice{{Quote: input.Body.Text(), Target: binding.Target{Class: "existing", ID: chosenRecord, Kind: "project"}}}, "person"); err != nil {
		t.Fatal(err)
	}
	missing := questionCall(t, tools, "revise_code_goal", "revise-missing", map[string]any{
		"goal_id": "missing-goal", "revision": chosenRevision,
		"outcome": "hijacked outcome", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if missing.Code != loop.Denied || !strings.Contains(string(missing.Output), "input_target_pending") {
		t.Fatalf("unresolved goal ran: %+v", missing)
	}
	revised := questionCall(t, tools, "revise_code_goal", "revise-other", map[string]any{
		"goal_id": otherID, "revision": otherRevision,
		"outcome": "hijacked outcome", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if revised.Code != loop.Denied || !strings.Contains(string(revised.Output), "input_target_pending") {
		t.Fatalf("revise_code_goal bypassed the chosen project: %+v", revised)
	}
	snap, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snap.Goals {
		if g.ID == otherID && g.Summary != "other outcome" {
			t.Fatalf("other goal changed: %+v", g)
		}
	}
	kept := questionCall(t, tools, "revise_code_goal", "revise-chosen", map[string]any{
		"goal_id": chosenID, "revision": chosenRevision,
		"outcome": "corrected chosen outcome", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if kept.Code != loop.OK {
		t.Fatalf("chosen goal stayed blocked: %+v", kept)
	}
}

func TestCreatedCodeGoalStaysOnSelectedProject(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	otherInput := questionPersonInput(t, r, "other-project", "Leave the other library alone.")
	otherTools := modelBindingTools(t, r, otherInput)
	input := questionPersonInput(t, r, "new-project", "Start the library work.")
	tools := modelBindingTools(t, r, input)
	admit := func(bindings map[string]loop.Tool, id, outcome string) (goalID, recordID, revision string) {
		t.Helper()
		got := questionCall(t, bindings, "dispatch_code_goal", id, map[string]any{
			"outcome": outcome, "done": "checks pass",
			"scope":             map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
			"criteria":          []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
			"no_service_reason": "library",
			"tasks":             []any{},
		})
		if got.Code != loop.OK {
			t.Fatalf("admit %s: %+v", id, got)
		}
		var fields map[string]string
		if err := json.Unmarshal(got.Output, &fields); err != nil {
			t.Fatal(err)
		}
		return fields["goal_id"], fields["record_id"], fields["revision"]
	}
	otherID, _, otherRevision := admit(otherTools, "other-goal", "other library")
	if _, err := r.saveModelBinding(t.Context(), input, "binding-person:new-project", []binding.Choice{{
		Quote:  input.Body.Text(),
		Target: binding.Target{Class: "new", Kind: "project"},
	}}, "person"); err != nil {
		t.Fatal(err)
	}
	chosenID, chosenRecord, chosenRevision := admit(tools, "created-goal", "library work")
	wantRecord, ok := goaleng.ProjectRecordID(chosenID)
	if !ok || chosenRecord != wantRecord {
		t.Fatalf("record id = %s", chosenRecord)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) != 1 || choices[0].CreatedID != chosenRecord || choices[0].Target.Kind != "project" {
		t.Fatalf("created identity: %+v %v", choices, err)
	}
	read := questionCall(t, tools, "read_record", "read-created", map[string]any{"id": chosenRecord})
	if read.Code != loop.OK {
		t.Fatalf("created record stayed blocked: %+v", read)
	}
	revised := questionCall(t, tools, "revise_code_goal", "revise-created", map[string]any{
		"goal_id": chosenID, "revision": chosenRevision,
		"outcome": "corrected library work", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if revised.Code != loop.OK {
		t.Fatalf("created goal stayed blocked: %+v", revised)
	}
	blocked := questionCall(t, tools, "revise_code_goal", "revise-other", map[string]any{
		"goal_id": otherID, "revision": otherRevision,
		"outcome": "hijacked library", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if blocked.Code != loop.Denied || !strings.Contains(string(blocked.Output), "input_target_pending") {
		t.Fatalf("other goal was reachable: %+v", blocked)
	}
}

func TestReadWorkProgressResolvesCodeGoalRecord(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	input := questionPersonInput(t, r, "progress-choice", "How is the library work going?")
	tools := modelBindingTools(t, r, input)
	admit := func(bindings map[string]loop.Tool, id, outcome string) (goalID, recordID string) {
		t.Helper()
		got := questionCall(t, bindings, "dispatch_code_goal", id, map[string]any{
			"outcome": outcome, "done": "checks pass",
			"scope":             map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
			"criteria":          []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
			"no_service_reason": "library",
			"tasks":             []any{},
		})
		if got.Code != loop.OK {
			t.Fatalf("admit %s: %+v", id, got)
		}
		var fields map[string]string
		if err := json.Unmarshal(got.Output, &fields); err != nil {
			t.Fatal(err)
		}
		return fields["goal_id"], fields["record_id"]
	}
	chosenID, chosenRecord := admit(tools, "progress-goal", "library work")
	wantRecord, ok := goaleng.ProjectRecordID(chosenID)
	if !ok || chosenRecord != wantRecord {
		t.Fatalf("record id = %s", chosenRecord)
	}
	if _, err := r.saveModelBinding(t.Context(), input, "binding-person:progress-choice", []binding.Choice{{
		Quote:  input.Body.Text(),
		Target: binding.Target{Class: "existing", ID: chosenRecord, Kind: "project"},
	}}, "person"); err != nil {
		t.Fatal(err)
	}
	read := questionCall(t, tools, "read_work_progress", "record-progress", map[string]string{"id": chosenRecord})
	if read.Code != loop.OK {
		t.Fatalf("progress read denied: %+v", read)
	}
	var body struct {
		ID       string `json:"id"`
		Progress struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"progress"`
	}
	if err := json.Unmarshal(read.Output, &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != chosenRecord {
		t.Fatalf("response id = %s, want the record id used for the binding check", body.ID)
	}
	if body.Progress.State == "unknown" || body.Progress.Name != "library work" {
		t.Fatalf("record id missed the progress line: %s", read.Output)
	}
	otherInput := questionPersonInput(t, r, "other-progress", "Leave the other library alone.")
	otherTools := modelBindingTools(t, r, otherInput)
	_, otherRecord := admit(otherTools, "other-progress-goal", "other library")
	blocked := questionCall(t, tools, "read_work_progress", "other-record", map[string]string{"id": otherRecord})
	if blocked.Code != loop.Denied || !strings.Contains(string(blocked.Output), "input_target_pending") {
		t.Fatalf("other record bypassed the chosen project: %+v", blocked)
	}
}

func TestReadWorkProgressResolvesTaskGeneration(t *testing.T) {
	r, _ := turnRuntime(t)
	input := questionPersonInput(t, r, "task-progress", "How is the library task going?")
	tools := modelBindingTools(t, r, input)
	card := work.Card{
		Task:  work.Task{ID: "library-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Library task"},
		State: "input_needed",
	}
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
	read := questionCall(t, tools, "read_work_progress", "generation-progress", map[string]string{"id": "task:library-task:3"})
	if read.Code != loop.OK {
		t.Fatalf("progress read denied: %+v", read)
	}
	var body struct {
		ID       string `json:"id"`
		Progress struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"progress"`
	}
	if err = json.Unmarshal(read.Output, &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "task:library-task:3" {
		t.Fatalf("response id = %s, want the original reference", body.ID)
	}
	if body.Progress.State == "unknown" || body.Progress.Name != "Library task" {
		t.Fatalf("task generation missed the progress line: %s", read.Output)
	}
	missing := questionCall(t, tools, "read_work_progress", "missing-generation", map[string]string{"id": "task:missing-task:1"})
	if missing.Code != loop.OK {
		t.Fatalf("missing task denied: %+v", missing)
	}
	if err = json.Unmarshal(missing.Output, &body); err != nil {
		t.Fatal(err)
	}
	if body.Progress.State != "unknown" {
		t.Fatalf("missing task was resolved: %s", missing.Output)
	}
}

func TestSubmitStepGraphHoldsTiedStart(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	input := questionPersonInput(t, r, "graph-tie", "Continue the chosen project.")
	tools := modelBindingTools(t, r, input)
	admit := func(id, outcome string) (goalID, recordID string) {
		t.Helper()
		got := questionCall(t, tools, "dispatch_code_goal", id, map[string]any{
			"outcome": outcome, "done": "checks pass",
			"scope":             map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
			"criteria":          []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
			"no_service_reason": "library",
			"tasks":             []any{},
		})
		if got.Code != loop.OK {
			t.Fatalf("admit %s: %+v", id, got)
		}
		var fields map[string]string
		if err := json.Unmarshal(got.Output, &fields); err != nil {
			t.Fatal(err)
		}
		return fields["goal_id"], fields["record_id"]
	}
	chosenID, chosenRecord := admit("graph-a", "chosen project")
	_, otherRecord := admit("graph-b", "other project")
	a := binding.Target{Class: "existing", ID: chosenRecord, Kind: "project"}
	b := binding.Target{Class: "existing", ID: otherRecord, Kind: "project"}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: []binding.Choice{{
		Quote: input.Body.Text(), Target: a, Tied: []binding.Target{a, b}, Action: "read",
	}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) != 1 || choices[0].Question != "" || len(choices[0].Tied) != 2 {
		t.Fatalf("read tie asked or dropped: %+v %v", choices, err)
	}
	got := questionCall(t, tools, "submit_step_graph", "tied-graph", map[string]any{
		"goal_id": chosenID, "expected_revision": 0,
		"steps": []any{map[string]any{
			"id": "change", "name": "Update library", "done": "checks pass",
			"repository": "repo", "base": "dev", "area": "src/**", "worker": "stub",
			"resources":  map[string]any{"cpu": 1, "memory_mb": 1, "disk_mb": 1},
			"depends_on": []any{},
		}},
	})
	if got.Code != loop.Denied || !strings.Contains(string(got.Output), "input_target_pending") {
		t.Fatalf("tied submission started work: %+v", got)
	}
	snap, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snap.Goals {
		if g.ID == chosenID && g.StepGraph != nil && g.StepGraph.Revision != 0 {
			t.Fatalf("ambiguous graph was admitted: %+v", g.StepGraph)
		}
	}
}

func TestWorkCompletionTurnKeepsBindingTools(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	event, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{
		ID: "completion-turn", ConversationID: r.conversation, AgentID: r.agent,
		Surface: ledger.Surface{Kind: "manager"}, ScaffoldKind: "work_completion",
		OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: `{"receipt":"saved"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &turnState{runtime: r, input: event, turnID: "turn:" + event.ID, bindings: map[string]loop.Tool{
		"read_record": {Validate: func(provider.ToolCall) (string, error) { return "record", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result {
			return loop.Result{Code: loop.OK}
		}},
	}}
	decls := s.prepareInputBinding([]provider.ToolDecl{provider.ToolDecl(`{"type":"function","name":"read_record","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}`)})
	if _, ok := s.bindings["bind_input"]; !ok || len(decls) < 2 {
		t.Fatal("work completion dropped binding tools")
	}
}

func TestPrepareWorkUsesSelectedChannelRuntime(t *testing.T) {
	r, _ := turnRuntime(t)
	channel := questionChannelInputs(t, r)
	view, steering := channel("prepare-source", "prepare-native")
	tui := questionPersonInput(t, r, "tui-prepare", "Prepare the channel task")
	if _, err := view.saveModelBinding(t.Context(), steering, "binding-model:channel-prepare", []binding.Choice{{Quote: steering.Body.Text(), Target: binding.Target{Class: "new", Kind: "task"}}}, "model"); err != nil {
		t.Fatal(err)
	}
	s := &turnState{runtime: r, input: tui, turnID: "turn:" + tui.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "prepare_work", "channel-prepare", map[string]any{
		"task_id": "channel-task", "title": "Create a report", "done": "Report exists", "workspace": map[string]any{},
		"input_binding": map[string]any{"input_event_id": steering.ID},
	})
	if got.Code != loop.OK {
		t.Fatalf("prepare: %+v", got)
	}
	card, err := r.work.Get(t.Context(), "channel-task")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := r.completionRunner(t.Context(), card)
	if err != nil || runner.channelBinding == nil || !runner.channelBinding.Scope.Tools {
		t.Fatalf("completion lost the channel binding: runner=%v err=%v", runner != nil && runner.channelBinding != nil, err)
	}
}
