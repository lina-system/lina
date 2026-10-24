// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// A worker session names the same item as its task. Once the person settles a
// target, a follow-up must not reach another task through a session reference,
// including a session retained across a restart.
func TestModelBindingSessionReferencesFollowTheChosenTask(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "task-a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
		{ID: "task-b", Title: "Second task", Kind: "task", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	foreign := &conversationRuntime{person: "person:elsewhere"}
	r.workers.mu.Lock()
	r.workers.sessions["session-a"] = &workerSession{thread: worker.Thread{ID: "session-a", TaskID: "task-a"}, origin: foreign}
	r.workers.mu.Unlock()
	t.Cleanup(func() {
		r.workers.mu.Lock()
		delete(r.workers.sessions, "session-a")
		r.workers.mu.Unlock()
	})
	// A session recorded before a restart keeps its task only in the recovery record.
	if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), recovery.Record{ID: "session-retained", Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: "input-retained", Thread: worker.Thread{ID: "session-retained", TaskID: "task-a"}, State: "active"}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "session-references", "Change the format.")
	_ = modelBindingTools(t, r, input)
	b := binding.Target{Class: "existing", ID: "task-b", Kind: "task"}
	if _, err := r.saveModelBinding(t.Context(), input, "session-person-choice", []binding.Choice{{Quote: input.Body.Text(), Target: b}}, "person"); err != nil {
		t.Fatal(err)
	}
	follow := func(id string) *loop.Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"id": id, "text": "Change the format."})
		return r.bindWorkCall(t.Context(), input, 0, "turn:"+input.ID, provider.ToolCall{ID: "follow-" + id, Name: "follow_work", Arguments: string(raw)})
	}
	for _, ref := range []string{"session:session-a", "session:session-retained", "task-a", "task:task-a:7", "task-a:7"} {
		// Any non-nil result means the follow-up was refused; the gate denies a
		// resolved target, and an unresolvable reference fails in the tool.
		if result := follow(ref); result == nil || result.Code == loop.OK {
			t.Fatalf("reference %q reached another task than the person chose: %+v", ref, result)
		}
	}
	if result := follow("task-b"); result != nil {
		t.Fatalf("the person's chosen task was refused: %+v", *result)
	}
}

// The worker control tools name a session, so the same resolution applies to
// them, including a session whose entry is gone after a restart.
func TestModelBindingWorkerSessionReferencesFollowTheChosenTask(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "task-a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
		{ID: "task-b", Title: "Second task", Kind: "task", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), recovery.Record{ID: "session-retained", Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: "input-retained", Thread: worker.Thread{ID: "session-retained", TaskID: "task-a"}, State: "active"}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "worker-session-references", "Change the format.")
	_ = modelBindingTools(t, r, input)
	b := binding.Target{Class: "existing", ID: "task-b", Kind: "task"}
	if _, err := r.saveModelBinding(t.Context(), input, "worker-session-person-choice", []binding.Choice{{Quote: input.Body.Text(), Target: b}}, "person"); err != nil {
		t.Fatal(err)
	}
	steer := func(session string) *loop.Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"session": session, "turn": "turn-1", "generation": 1, "binding": 1, "text": "Change the format."})
		return r.bindWorkCall(t.Context(), input, 0, "turn:"+input.ID, provider.ToolCall{ID: "steer-" + session, Name: "worker_input", Arguments: string(raw)})
	}
	if result := steer("session-retained"); result == nil || result.Code != loop.Denied {
		t.Fatalf("a retained session for another task accepted the person's request: %+v", result)
	}
	if result := steer("session-b"); result != nil {
		t.Fatalf("the person's chosen task was refused: %+v", *result)
	}
}

// A follow_work to a session kept only in the recovery record still has a task
// card. That must be stored for the worker's return, not refused as a live steer.
func TestFollowWorkRetainedSessionKeepsOfflineDelivery(t *testing.T) {
	r, _ := turnRuntime(t)
	now := time.Now().UTC()
	state := binding.State{Clock: now, Focus: binding.Target{Class: "none"}, Items: []binding.Item{
		{ID: "task-a", Title: "First task", Kind: "task", State: "running", LastActivity: now},
	}, Surfaces: map[string]string{}}
	if err := r.seedIntakeReplay(t.Context(), state, binding.Input{}); err != nil {
		t.Fatal(err)
	}
	if err := (recovery.Recovery{DB: r.db}).Put(t.Context(), recovery.Record{ID: "session-retained", Person: r.person, Agent: r.agent, Conversation: r.conversation, Input: "input-retained", Thread: worker.Thread{ID: "session-retained", TaskID: "task-a"}, State: "active"}); err != nil {
		t.Fatal(err)
	}
	input := questionPersonInput(t, r, "retained-follow", "Change the format.")
	tools := modelBindingTools(t, r, input)
	for i := range 13 {
		got := questionCall(t, tools, "follow_work", "retained-"+string(rune('a'+i)), map[string]string{"id": "session:session-retained", "text": "Change the format."})
		if i < 12 && got.Code != loop.OK {
			t.Fatalf("retained session follow-up %d: %+v", i, got)
		}
		if i == 12 && got.Code == loop.OK {
			t.Fatal("another retained follow-up replenished the coordination allowance")
		}
	}
	messages, err := r.work.SteeringMessages(t.Context(), "codex:session-retained")
	if err != nil || len(messages) != 12 || messages[0].Text != "Change the format." || messages[0].TaskID != "task-a" || messages[0].SpeakerKind != "agent" || messages[0].Sender != r.agent {
		t.Fatalf("offline delivery: %+v %v", messages, err)
	}
}
