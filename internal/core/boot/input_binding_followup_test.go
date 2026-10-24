// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	goaleng "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
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

func TestCreatedReceiptsLeaveMultipleTargetsUnresolved(t *testing.T) {
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
	if decision.Target.Class != "none" || decision.FocusFresh {
		t.Fatalf("multiple created tasks became one focus: %+v", decision)
	}
}

func TestInputChoicesProjectOneFocus(t *testing.T) {
	a := binding.Target{Class: "existing", ID: "focus-a", Kind: "task"}
	b := binding.Target{Class: "existing", ID: "focus-b", Kind: "task"}
	none := binding.Target{Class: "none"}
	for _, tc := range []struct {
		name    string
		choices []binding.Choice
		want    binding.Target
	}{
		{"single", []binding.Choice{{Target: a}}, a},
		{"same item", []binding.Choice{{Target: a}, {Target: a}}, a},
		{"distinct", []binding.Choice{{Target: a}, {Target: b}}, none},
		{"reversed", []binding.Choice{{Target: b}, {Target: a}}, none},
		{"single created", []binding.Choice{{Target: binding.Target{Class: "new", Kind: "task"}, CreatedID: a.ID}}, a},
		{"created and existing same item", []binding.Choice{{Target: binding.Target{Class: "new", Kind: "task"}, CreatedID: a.ID}, {Target: a}}, a},
		{"tie then settled", []binding.Choice{{Target: a, Tied: []binding.Target{a, b}}, {Target: a}}, none},
		{"settled then tie", []binding.Choice{{Target: a}, {Target: a, Tied: []binding.Target{a, b}}}, none},
		{"question then settled", []binding.Choice{{Target: a, Question: "pending"}, {Target: a}}, none},
		{"cancelled then settled", []binding.Choice{{Target: b, Cancelled: true}, {Target: a}}, none},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := controlRuntime(t)
			input := questionPersonInput(t, r, "focus-parent", "Check both tasks")
			for n := range tc.choices {
				tc.choices[n].Request = n
				tc.choices[n].Quote = input.Body.Text()
			}
			if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
				return appendInputChoices(t.Context(), tx, input, "focus-choices", tc.choices)
			}); err != nil {
				t.Fatal(err)
			}
			next := questionPersonInput(t, r, "focus-next", "stop it")
			var state binding.State
			if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
				var err error
				state, err = r.bindingState(t.Context(), tx, next)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if state.Focus != tc.want {
				t.Fatalf("focus = %+v, want %+v", state.Focus, tc.want)
			}
			decision := binding.Resolve(state, binding.Input{Text: "stop it"})
			if decision.Target != tc.want || decision.FocusFresh != (tc.want.Class == "existing") {
				t.Fatalf("follow-up = %+v, want %+v", decision, tc.want)
			}
		})
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
	otherRecord, ok := goaleng.ProjectRecordID(otherID)
	if !ok {
		t.Fatal("other record id")
	}
	prefixedOther := questionCall(t, tools, "revise_code_goal", "revise-other-record", map[string]any{
		"goal_id": otherRecord, "revision": otherRevision,
		"outcome": "hijacked outcome", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if prefixedOther.Code != loop.Denied || !strings.Contains(string(prefixedOther.Output), "input_target_pending") {
		t.Fatalf("record id of goal B bypassed project A: %+v", prefixedOther)
	}
	var chosenNow string
	snap, err = r.workEngine.engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snap.Goals {
		if g.ID == chosenID {
			chosenNow = g.RecordRevision
		}
	}
	prefixedChosen := questionCall(t, tools, "revise_code_goal", "revise-chosen-record", map[string]any{
		"goal_id": chosenRecord, "revision": chosenNow,
		"outcome": "corrected chosen outcome again", "done": "checks pass",
		"scope":    map[string]any{"repositories": []string{"repo"}, "includes": "library", "excludes": "service"},
		"criteria": []any{map[string]any{"id": "checked", "statement": "library works", "check": map[string]string{"kind": "check", "method": "acceptance", "target": "repo"}}},
	})
	if prefixedChosen.Code != loop.OK {
		t.Fatalf("record id of project A stayed blocked: %+v", prefixedChosen)
	}
}

func TestReplyToReadTieStaysUnresolved(t *testing.T) {
	r, _ := turnRuntime(t)
	reserve := t.TempDir()
	first := work.Task{ID: "reply-a", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "First task", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	second := work.Task{ID: "reply-b", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Second task", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	for _, task := range []work.Task{first, second} {
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "reply-b-run", TaskID: second.ID, Generation: 1, Worker: "codex", ThreadID: "reply-b-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "reply-b-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "reply-b-thread", TaskID: second.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "reply-b-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	text := "check its progress and stop it"
	parent := questionPersonInput(t, r, "reply-tie", text)
	tools := modelBindingTools(t, r, parent)
	a := binding.Target{Class: "existing", Kind: "task", ID: first.ID}
	b := binding.Target{Class: "existing", Kind: "task", ID: second.ID}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: []binding.Choice{{Quote: text, Target: a, Tied: []binding.Target{a, b}, Action: "read"}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	now := time.Now().UTC()
	reply, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "reply-stop", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui", ReplyTo: parent.ID}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "stop it",
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
	if !decision.Unresolved || decision.Rule == "reply-or-card" || decision.Target.ID == first.ID || decision.Target.ID == second.ID {
		t.Fatalf("read tie settled the reply: %+v", decision)
	}
	replyTools := modelBindingTools(t, r, reply)
	stopped := questionCall(t, replyTools, "worker_interrupt", "stop-reply", map[string]any{"session": thread.ID, "turn": "old", "generation": 1, "binding": 1, "quote": "stop it"})
	if stopped.Code != loop.Denied || !strings.Contains(string(stopped.Output), "input_target_pending") || probe.interrupts != 0 {
		t.Fatalf("reply interrupt skipped the tie: %+v interrupts=%d", stopped, probe.interrupts)
	}
	oneText := "stay with the first task"
	one := questionPersonInput(t, r, "reply-one", oneText)
	oneTools := modelBindingTools(t, r, one)
	if got := questionCall(t, oneTools, "bind_input", "one-target", bindingRequest{Bindings: []binding.Choice{{Quote: oneText, Target: a}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	settled, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "reply-settled", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: ledger.Surface{Kind: "tui", ReplyTo: one.ID}, OccurredAt: now.Add(time.Second), RecordedAt: now.Add(time.Second), TimeQuality: "observed", Body: "continue",
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err = r.journal().Get(t.Context(), inputBindingID(settled.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(saved.Body.Text()), &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Target.ID != first.ID || decision.Decider != "reply-or-card" {
		t.Fatalf("untied choice did not settle the reply: %+v", decision)
	}
}

func TestTiedChoiceClearsAdmissionFocus(t *testing.T) {
	testChoiceClearsAdmissionFocus(t, false)
}

func TestMultiTargetChoiceClearsAdmissionFocus(t *testing.T) {
	testChoiceClearsAdmissionFocus(t, true)
}

func testChoiceClearsAdmissionFocus(t *testing.T, split bool) {
	t.Helper()
	r, _ := turnRuntime(t)
	reserve := t.TempDir()
	first := work.Task{ID: "focus-a", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Alpha", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	second := work.Task{ID: "focus-b", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Beta", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	for _, task := range []work.Task{first, second} {
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "focus-a-run", TaskID: first.ID, Generation: 1, Worker: "codex", ThreadID: "focus-a-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "focus-a-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "focus-a-thread", TaskID: first.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "focus-a-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	seed := questionPersonInput(t, r, "focus-seed", first.Title)
	seeded, err := r.journal().Get(t.Context(), inputBindingID(seed.ID))
	if err != nil {
		t.Fatal(err)
	}
	var seededDecision binding.Decision
	if err = json.Unmarshal([]byte(seeded.Body.Text()), &seededDecision); err != nil {
		t.Fatal(err)
	}
	if seededDecision.Target.ID != first.ID {
		t.Fatalf("admission did not settle on A: %+v", seededDecision)
	}
	text := "check its progress"
	parent := questionPersonInput(t, r, "focus-tie", text)
	admitted, err := r.journal().Get(t.Context(), inputBindingID(parent.ID))
	if err != nil {
		t.Fatal(err)
	}
	var admittedDecision binding.Decision
	if err = json.Unmarshal([]byte(admitted.Body.Text()), &admittedDecision); err != nil {
		t.Fatal(err)
	}
	if admittedDecision.Target.ID != first.ID {
		t.Fatalf("fresh focus did not keep A: %+v", admittedDecision)
	}
	tools := modelBindingTools(t, r, parent)
	a := binding.Target{Class: "existing", Kind: "task", ID: first.ID}
	b := binding.Target{Class: "existing", Kind: "task", ID: second.ID}
	choices := []binding.Choice{{Quote: text, Target: a, Tied: []binding.Target{a, b}, Action: "read"}}
	if split {
		choices = []binding.Choice{{Quote: "check", Target: b}, {Request: 1, Quote: "its progress", Target: a}}
	}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: choices}); got.Code != loop.OK {
		t.Fatal(got)
	}
	now := time.Now().UTC()
	surface := ledger.Surface{Kind: "tui", ReplyTo: parent.ID}
	if split {
		surface.ReplyTo = ""
	}
	reply, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: "focus-stop", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation,
		Surface: surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "stop it",
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
	if !decision.Unresolved || decision.Rule == "reply-or-card" || decision.FocusFresh || decision.Target.ID == first.ID || decision.Target.ID == second.ID {
		t.Errorf("ambiguous choices kept admission focus: %+v", decision)
	}
	replyTools := modelBindingTools(t, r, reply)
	stopped := questionCall(t, replyTools, "worker_interrupt", "stop-focus", map[string]any{"session": thread.ID, "turn": "old", "generation": 1, "binding": 1, "quote": "stop it"})
	if stopped.Code != loop.Denied || !strings.Contains(string(stopped.Output), "input_target_pending") || probe.interrupts != 0 {
		t.Fatalf("reply interrupted A without a target question: %+v interrupts=%d", stopped, probe.interrupts)
	}
	pending, err := waiting.NewConversationQuestions(r.db).Pending(t.Context(), r.person, r.agent)
	if err != nil || len(pending) != 1 || pending[0].Binding == nil || len(pending[0].Choices) != 2 {
		t.Fatalf("missing target question: %+v %v", pending, err)
	}
}

func TestInterruptTieAsksBeforeCancel(t *testing.T) {
	r, _ := turnRuntime(t)
	reserve := t.TempDir()
	first := work.Task{ID: "tie-a", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "First task", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	second := work.Task{ID: "tie-b", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Second task", Done: "Done", ArtifactRoots: []string{t.TempDir()}}
	for _, task := range []work.Task{first, second} {
		if err := r.work.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "tie-b-run", TaskID: second.ID, Generation: 1, Worker: "codex", ThreadID: "tie-b-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "tie-b-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "tie-b-thread", TaskID: second.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "tie-b-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	text := "check its progress and stop it"
	input := questionPersonInput(t, r, "interrupt-tie", text)
	tools := modelBindingTools(t, r, input)
	a := binding.Target{Class: "existing", Kind: "task", ID: first.ID}
	b := binding.Target{Class: "existing", Kind: "task", ID: second.ID}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: []binding.Choice{{Quote: text, Target: a, Tied: []binding.Target{a, b}, Action: "read"}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	stopped := questionCall(t, tools, "worker_interrupt", "stop-tied", map[string]any{"session": thread.ID, "turn": "old", "generation": 1, "binding": 1, "quote": text})
	if stopped.Code != loop.Denied || !strings.Contains(string(stopped.Output), "input_target_pending") {
		t.Fatalf("tied interrupt ran: %+v", stopped)
	}
	if probe.interrupts != 0 {
		t.Fatalf("tied interrupt cancelled the worker %d times", probe.interrupts)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) == 0 || choices[0].Question == "" {
		t.Fatalf("tie did not ask: %+v %v", choices, err)
	}
	resolvedText := "stop the other task"
	resolved := questionPersonInput(t, r, "interrupt-one", resolvedText)
	resolvedTools := modelBindingTools(t, r, resolved)
	if _, err := r.saveModelBinding(t.Context(), resolved, "binding-person:interrupt-one", []binding.Choice{{Quote: resolvedText, Target: b}}, "person"); err != nil {
		t.Fatal(err)
	}
	one := questionCall(t, resolvedTools, "worker_interrupt", "stop-one", map[string]any{"session": thread.ID, "turn": "old", "generation": 1, "binding": 1, "quote": resolvedText})
	if one.Code != loop.OK {
		t.Fatalf("resolved interrupt refused: %+v", one)
	}
	if probe.interrupts != 1 {
		t.Fatalf("resolved interrupt count = %d", probe.interrupts)
	}
}

func TestSubmitStepGraphAsksBeforeStartingTiedWork(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	text := "check its progress and stop it"
	input := questionPersonInput(t, r, "graph-tie", text)
	tools := modelBindingTools(t, r, input)
	admit := func(id, outcome string) string {
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
		return fields["goal_id"]
	}
	firstID := admit("graph-a", "first project outcome")
	secondID := admit("graph-b", "second project outcome")
	firstRecord, ok := goaleng.ProjectRecordID(firstID)
	secondRecord, ok2 := goaleng.ProjectRecordID(secondID)
	if !ok || !ok2 {
		t.Fatal("record id")
	}
	a := binding.Target{Class: "existing", Kind: "project", ID: firstRecord}
	b := binding.Target{Class: "existing", Kind: "project", ID: secondRecord}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: []binding.Choice{{Quote: text, Target: a, Tied: []binding.Target{a, b}, Action: "read"}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	steps := []any{map[string]any{
		"id": "change", "name": "Update library", "done": "checks pass",
		"repository": "repo", "base": "dev", "area": "src/**", "worker": "stub",
		"resources":  map[string]any{"cpu": 1, "memory_mb": 1, "disk_mb": 1},
		"depends_on": []any{},
	}}
	submitted := questionCall(t, tools, "submit_step_graph", "start-tied", map[string]any{"goal_id": secondID, "expected_revision": 0, "steps": steps})
	if submitted.Code != loop.Denied || !strings.Contains(string(submitted.Output), "input_target_pending") {
		t.Fatalf("tied graph started: %+v", submitted)
	}
	graph, err := r.workEngine.engine.ReadStepGraph(t.Context(), secondID, r.person, r.agent, r.conversation)
	if err != nil || graph.Revision != 0 || len(graph.Steps) != 0 {
		t.Fatalf("tied submission wrote a graph: %+v %v", graph, err)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) == 0 || choices[0].Question == "" {
		t.Fatalf("tie did not ask: %+v %v", choices, err)
	}
	resolvedText := "start the second project"
	resolved := questionPersonInput(t, r, "graph-one", resolvedText)
	resolvedTools := modelBindingTools(t, r, resolved)
	if _, err := r.saveModelBinding(t.Context(), resolved, "binding-person:graph-one", []binding.Choice{{Quote: resolvedText, Target: b}}, "person"); err != nil {
		t.Fatal(err)
	}
	one := questionCall(t, resolvedTools, "submit_step_graph", "start-one", map[string]any{"goal_id": secondID, "expected_revision": 0, "steps": steps})
	if one.Code != loop.OK {
		t.Fatalf("resolved graph refused: %+v", one)
	}
	graph, err = r.workEngine.engine.ReadStepGraph(t.Context(), secondID, r.person, r.agent, r.conversation)
	if err != nil || graph.Revision == 0 || len(graph.Steps) != 1 {
		t.Fatalf("resolved graph missing: %+v %v", graph, err)
	}
}

func TestFollowWorkStaysInsideWorkerConversation(t *testing.T) {
	requester, _ := turnRuntime(t)
	dev := workAgentFixture(t, requester)
	owner := newConversationView(requester, dev, "manager:unit")
	owner.codingDelivery = requester
	dir := t.TempDir()
	reserve := t.TempDir()
	task := work.Task{ID: "owned-task", PersonID: owner.person, AgentID: owner.agent, ConversationID: owner.conversation, Title: "Owned task", Done: "Done", ArtifactRoots: []string{dir}}
	if err := requester.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := requester.work.Start(t.Context(), work.Run{ID: "owned-run", TaskID: task.ID, Generation: 1, Worker: "codex", ThreadID: "owned-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := requester.work.ReserveWorker(t.Context(), "owned-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "owned-thread", TaskID: task.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &messageProbe{transferProbe: &transferProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, events: make(chan worker.Event, 1), next: "old"}}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: owner, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "owned-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	requester.workers.mu.Lock()
	requester.workers.sessions[thread.ID] = entry
	requester.workers.mu.Unlock()
	t.Cleanup(func() {
		requester.workers.mu.Lock()
		delete(requester.workers.sessions, thread.ID)
		requester.workers.mu.Unlock()
	})
	follow := func(view *conversationRuntime, id, text string) loop.Result {
		t.Helper()
		input := questionPersonInput(t, view, id, text)
		tools := modelBindingTools(t, view, input)
		return questionCall(t, tools, "follow_work", id, map[string]string{"id": "task:" + task.ID + ":1", "text": text})
	}
	samePerson := newConversationView(requester, requester.agent, "unrelated")
	otherPerson := newConversationView(requester, requester.agent, "foreign-person")
	otherPerson.person = "someone-else"
	for _, foreign := range []*conversationRuntime{samePerson, otherPerson} {
		got := follow(foreign, "foreign-"+foreign.conversation, "Steer from outside")
		if got.Code != loop.Denied {
			t.Fatalf("%s follow reached the worker: %+v", foreign.conversation, got)
		}
	}
	if probe.inputs != 0 {
		t.Fatalf("unrelated follow-ups delivered %d inputs", probe.inputs)
	}
	owned := follow(owner, "owner-follow", "Steer as the owner")
	if owned.Code != loop.OK || probe.inputs != 1 || !strings.Contains(probe.text, `sender="`+owner.agent+`"`) {
		t.Fatalf("owner follow: code=%s inputs=%d text=%s", owned.Code, probe.inputs, probe.text)
	}
	requested := follow(requester, "requester-follow", "Steer as the requester")
	if requested.Code != loop.OK || probe.inputs != 2 || !strings.Contains(probe.text, `sender="`+requester.agent+`"`) || strings.Contains(probe.text, `sender="`+owner.agent+`"`) {
		t.Fatalf("requester follow: code=%s inputs=%d text=%s", requested.Code, probe.inputs, probe.text)
	}
}

func TestBuildMockAsksBeforeAdmittingTiedWork(t *testing.T) {
	r, _ := turnRuntime(t)
	dev := workAgentFixture(t, r)
	r.agent, r.conversation = dev, "main:"+dev
	r.router.views[dev] = r
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r.records = repo
	if err = r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	store, err := r.resultStore(ledger.Surface{Kind: "app"})
	if err != nil {
		t.Fatal(err)
	}
	run := "mock-run"
	scope := result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: "mock-turn", TaskID: &run}
	body := result.Mock{Request: "Show tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: "png", Revision: 1}, StopReason: "all_passed"}
	for _, id := range []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"} {
		body.Checks = append(body.Checks, result.MockCheck{ID: id, Passed: true})
	}
	raw, _ := json.Marshal(body)
	presented, err := store.PresentMock(t.Context(), scope, result.PresentRequest{Type: result.MockType, Title: "Mock", Summary: "Checked", Body: raw}, run)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "mock-first", Title: "First analysis", Kind: "run", State: "running", LastActivity: now},
		{ID: "mock-second", Title: "Second analysis", Kind: "run", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err = r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	text := "Check its progress and build the mock."
	input := questionPersonInput(t, r, "mock-tie", text)
	tools := modelBindingTools(t, r, input)
	a := binding.Target{Class: "existing", Kind: "run", ID: "mock-first"}
	b := binding.Target{Class: "existing", Kind: "run", ID: "mock-second"}
	if got := questionCall(t, tools, "bind_input", "read-tie", bindingRequest{Bindings: []binding.Choice{{Quote: text, Target: a, Tied: []binding.Target{a, b}, Action: "read"}}}); got.Code != loop.OK {
		t.Fatal(got)
	}
	built := questionCall(t, tools, "build_mock", "build-tied", mockBuildInput{ResultID: presented.ID, Revision: presented.Revision, Target: "screen", Repositories: []string{"example/project"}})
	if built.Code != loop.Denied || !strings.Contains(string(built.Output), "input_target_pending") {
		t.Fatalf("tied mock build admitted work: %+v", built)
	}
	choices, err := r.currentInputChoices(t.Context(), input)
	if err != nil || len(choices) == 0 || choices[0].Question == "" {
		t.Fatalf("tie did not ask: %+v %v", choices, err)
	}
	snapshot, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil || len(snapshot.Goals) != 0 {
		t.Fatalf("tied build reached Admit: goals=%d err=%v", len(snapshot.Goals), err)
	}
}

func TestBindingActionClassifiesEveryWrappedTool(t *testing.T) {
	r, _ := turnRuntime(t)
	if err := r.startWork(t.Context(), &WorkConfig{Driver: func(*goaleng.Engine) (goaleng.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.workEngine.close(t.Context()) })
	input := questionPersonInput(t, r, "tool-class", "Which tools can change work?")
	tools := modelBindingTools(t, r, input)
	got := map[string]string{}
	for name := range tools {
		if name == "bind_input" || name == "resolve_question" || name == "ask_question" {
			continue
		}
		got[name] = bindingAction(name)
	}
	want := map[string]string{
		"begin_artifact_change":  "send",
		"build_mock":             "start",
		"calculate":              "",
		"call_tool":              "start",
		"cancel_timer":           "stop",
		"change_persona":         "",
		"create_timer":           "",
		"dispatch_code_goal":     "start",
		"dispatch_work":          "start",
		"draft_plan":             "",
		"fill_persona_setting":   "",
		"fill_work_method":       "",
		"finish_artifact_change": "",
		"follow_work":            "steer",
		"handoff_status":         "",
		"land_work_result":       "",
		"list_candidates":        "",
		"load_tools":             "",
		"mini_app":               "",
		"observe_health":         "",
		"plan_index":             "",
		"prepare_work":           "",
		"present_result":         "",
		"present_surface":        "",
		"promote_memo":           "",
		"propose_candidate":      "",
		"query_records":          "",
		"read_plan":              "",
		"read_record":            "",
		"read_result":            "",
		"read_skill":             "",
		"read_step_graph":        "",
		"read_work_progress":     "",
		"record_emotion":         "",
		"record_relationship":    "",
		"remember_name":          "",
		"remote_agent":           "send",
		"repair_records_index":   "",
		"report_work_result":     "",
		"retain_work_result":     "",
		"revise_code_goal":       "start",
		"send_handoff":           "send",
		"set_emotion_expression": "",
		"skill_index":            "",
		"skill_notices":          "",
		"start_run":              "start",
		"submit_step_graph":      "start",
		"timer_status":           "",
		"undo_persona":           "",
		"undo_record":            "",
		"undo_skill":             "",
		"worker_input":           "steer",
		"worker_interrupt":       "stop",
		"worker_quota":           "",
		"worker_read_file":       "",
		"worker_snapshot":        "",
		"worker_start":           "start",
		"write_record":           "",
		"write_skill":            "",
	}
	if len(got) != len(want) {
		t.Fatalf("wrapped tools = %v", got)
	}
	for name, action := range want {
		if got[name] != action {
			t.Errorf("%s action = %q, want %q", name, got[name], action)
		}
	}
}

func TestStartRunKeepsChannelSteeringReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	app, inboxID := admitTurnInput(t, r, "app-turn")
	channel := questionChannelInputs(t, r)
	view, steering := channel("one", "first")
	s := &turnState{runtime: r, input: app, turnID: "turn:" + app.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "start_run", "from-app", map[string]any{
		"Goal": "Research the draft", "Scope": "Read the catalog", "NotTouch": "Do not send",
		"Done": "A short note exists", "Result": "summary", "Allow": []string{},
		"Checks":        []map[string]string{{"Name": "note", "Kind": "claimed"}},
		"input_binding": map[string]any{"input_event_id": steering.ID},
	})
	if got.Code != loop.OK {
		t.Fatalf("channel steering start: %+v", got)
	}
	var receipt struct{ ID string }
	if json.Unmarshal(got.Output, &receipt) != nil || receipt.ID == "" {
		t.Fatalf("receipt: %s", got.Output)
	}
	record, err := r.background.service.Get(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Origin.Turn != "turn:"+steering.ID {
		t.Fatalf("run kept the app origin %s", record.Origin.Turn)
	}
	if err = r.authorizeRun(t.Context(), record.Origin, record.Brief); err != nil {
		t.Fatal(err)
	}
	revoked := *view.channelBinding
	revoked.Scope.Tools = false
	if err = r.channels.store.Bind(t.Context(), revoked); err != nil {
		t.Fatal(err)
	}
	if err = r.authorizeRun(t.Context(), record.Origin, record.Brief); err == nil {
		t.Fatal("revoked channel still authorized the queued run")
	}
	revoked.Scope.Tools = true
	if err = r.channels.store.Bind(t.Context(), revoked); err != nil {
		t.Fatal(err)
	}
	turns := turn.New(r.db)
	if err = turns.Claim(t.Context(), s.turnID, r.conversation, inboxID); err != nil {
		t.Fatal(err)
	}
	call := provider.ToolCall{ID: "from-app", Name: "start_run", Arguments: "{}"}
	if err = turns.Journal(s.turnID).Calls(t.Context(), 0, []provider.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err = turns.Journal(s.turnID).Result(t.Context(), 0, 0, got); err != nil {
		t.Fatal(err)
	}
	if err = r.syncRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, err = r.background.service.Get(t.Context(), receipt.ID)
	if err != nil || !record.Released {
		t.Fatalf("committed channel start was not released: %+v %v", record, err)
	}
}

func TestWorkerInputKeepsChannelSteeringReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	app := questionPersonInput(t, r, "app-turn", "Tell the worker to continue")
	channel := questionChannelInputs(t, r)
	view, steering := channel("one", "first")
	th := worker.Thread{ID: "session-c", TaskID: "channel-worker", AttemptID: "attempt", TurnID: "turn-1", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: th, State: worker.Active}}, tier: "turn"}
	session := &workerSession{origin: r, thread: th, adapter: probe}
	r.workers.mu.Lock()
	r.workers.sessions[th.ID] = session
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, th.ID)
		r.workers.mu.Unlock()
	})
	s := &turnState{runtime: r, input: app, turnID: "turn:" + app.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "worker_input", "steer", map[string]any{
		"session": th.ID, "turn": th.TurnID, "generation": th.ProcessGeneration, "binding": th.BindingEpoch,
		"text": "Keep the channel scope.", "input_binding": map[string]any{"input_event_id": steering.ID},
	})
	if got.Code != loop.OK {
		t.Fatalf("channel worker input: %+v", got)
	}
	messages, err := r.work.SteeringMessages(t.Context(), probe.Name()+":"+th.ID)
	if err != nil || len(messages) != 1 || len(messages[0].Source) == 0 {
		t.Fatalf("worker input lost its channel receipt: %+v %v", messages, err)
	}
	revoked := *view.channelBinding
	revoked.Scope.Tools = false
	if err = r.channels.store.Bind(t.Context(), revoked); err != nil {
		t.Fatal(err)
	}
	event := worker.Event{Thread: th, ToolCall: &worker.ToolCall{ID: "inbox", Namespace: "lina", Name: "check_inbox", Arguments: json.RawMessage(`{}`)}}
	raw, _, err := session.mailboxReply(t.Context(), event, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Keep the channel scope.") {
		t.Fatal("revoked channel delivered the worker input")
	}
}

func TestFollowWorkKeepsChannelTaskReceipt(t *testing.T) {
	r, _ := turnRuntime(t)
	dir := t.TempDir()
	reserve := t.TempDir()
	task := work.Task{ID: "channel-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Channel task", Done: "Done", ArtifactRoots: []string{dir}}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "channel-task-run", TaskID: task.ID, Generation: 1, Worker: "codex", ThreadID: "channel-thread", TurnID: "old", NodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.ReserveWorker(t.Context(), "channel-worker", reserve); err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "channel-thread", TaskID: task.ID, TurnID: "old", ProcessGeneration: 1, BindingEpoch: 1}
	probe := &mailboxProbe{workerProbe: workerProbe{snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}, tier: "turn"}
	done := make(chan struct{})
	close(done)
	entry := &workerSession{adapter: probe, origin: r, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, thread: thread, workdir: reserve, reservation: "channel-worker", process: lease.New(func() {}), done: done, active: map[string]lease.Lease{}, close: func() error { return nil }}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = entry
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
	})
	app := questionPersonInput(t, r, "app-follow", "Continue the channel task")
	channel := questionChannelInputs(t, r)
	view, steering := channel("task-source", "task-native")
	s := &turnState{runtime: r, input: app, turnID: "turn:" + app.ID, bindings: map[string]loop.Tool{}, questions: &questionTurn{}, bindingInputs: map[string]acceptedSteering{steering.ID: {event: steering, runner: view}}}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	got := questionCall(t, s.bindings, "follow_work", "task-note", map[string]any{"id": "task:" + task.ID + ":1", "text": "Keep the channel scope.", "input_binding": map[string]any{"input_event_id": steering.ID}})
	if got.Code != loop.OK {
		t.Fatalf("channel task follow-up: %+v", got)
	}
	messages, err := r.work.SteeringMessages(t.Context(), probe.Name()+":"+thread.ID)
	if err != nil || len(messages) != 1 || len(messages[0].Source) == 0 {
		t.Fatalf("task follow-up lost its channel receipt: %+v %v", messages, err)
	}
}
