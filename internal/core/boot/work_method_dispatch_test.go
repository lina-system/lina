// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestWorkMethodDispatchDisabled(t *testing.T) {
	r := controlRuntime(t)
	if err := r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "dev", WorkMethod: `{"schema":1,"preset":"dev","handoff":{"owns":["code"]},"workers":{"dispatch":false}}`}, r.person); err != nil {
		t.Fatal(err)
	}
	_, err := r.assignCoding(t.Context(), dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "method-turn"}, "method-disabled", "worker_start", "/fixture", json.RawMessage(`{}`))
	if !errors.Is(err, errDevUnavailable) {
		t.Fatalf("disabled dev must not own a worker task: %v", err)
	}
}

func TestWorkMethodCodeExchange(t *testing.T) {
	r := controlRuntime(t)
	origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "method-turn"}
	a, err := r.assignCoding(t.Context(), origin, "method-exchange", "worker_start", "/fixture", json.RawMessage(`{"task_packet":"Implement the fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = r.assignCoding(t.Context(), origin, a.ID, a.Tool, a.Target, a.Arguments); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := exchange.NewWithMaterials(r.db, r.materials).Log(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].From != r.agent || lines[0].To != a.Owner || lines[0].State != exchange.Delivered {
		t.Fatalf("code handoff has no single delivered exchange: %+v", lines)
	}
}

func TestWorkMethodGoalResultReturnsToRequester(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "method-request", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "app"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the result"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.assignCoding(t.Context(), dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:" + input.ID}, "method-goal", "dispatch_code_goal", "fixture", json.RawMessage(`{"outcome":"Create the result"}`))
	if err != nil {
		t.Fatal(err)
	}
	g := goal.Goal{ID: a.ID, Person: r.person, Agent: a.Owner, Conversation: a.Conversation, Turn: "coding:" + a.ID, Summary: "Create the result", RecordRevision: "fixture", State: goal.GoalBlocked}
	for range 2 {
		if err = r.publishCodeGoals(t.Context(), goal.State{Goals: []goal.Goal{g}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := r.journal().Get(t.Context(), "goal-result:"+codingID(g.ID, string(g.State), g.RecordRevision))
	if err != nil || result.ConversationID != r.conversation || result.AgentID != r.agent || result.ParentID != input.ID {
		t.Fatalf("goal result lost origin: %+v %v", result, err)
	}
}

// installDispatchRecord gives the dev preset the record that owns code and
// enables worker dispatch, for fixtures that exercise dispatch directly.
func installDispatchRecord(t *testing.T, r *conversationRuntime) {
	t.Helper()
	method := `{"schema":1,"preset":"dev","tools":{"declare_first":["work","github","terminal"]},"workers":{"dispatch":true},"handoff":{"owns":["code","pull_request","worker_task"]}}`
	if err := r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "dev", WorkMethod: method}, r.person); err != nil {
		t.Fatal(err)
	}
}

func workAgentFixture(t *testing.T, r *conversationRuntime) string {
	t.Helper()
	installDispatchRecord(t, r)
	all, err := r.agents.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.Preset == "dev" {
			return a.ID
		}
	}
	t.Fatal("dev fixture missing")
	return ""
}

func TestRejectedPersonLineDoesNotStopTheWorkRunner(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "method-request-rejected", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "app"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the result"})
	if err != nil {
		t.Fatal(err)
	}
	origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:" + input.ID}
	bad, err := r.assignCoding(t.Context(), origin, "method-goal-bad", "dispatch_code_goal", "fixture", json.RawMessage(`{"outcome":"Write result.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	good, err := r.assignCoding(t.Context(), origin, "method-goal-good", "dispatch_code_goal", "fixture", json.RawMessage(`{"outcome":"Create the result"}`))
	if err != nil {
		t.Fatal(err)
	}
	goals := []goal.Goal{
		{ID: bad.ID, Person: r.person, Agent: bad.Owner, Conversation: bad.Conversation, Turn: "coding:" + bad.ID, Summary: "Write result.txt", RecordRevision: "fixture", State: goal.GoalBlocked},
		{ID: good.ID, Person: r.person, Agent: good.Owner, Conversation: good.Conversation, Turn: "coding:" + good.ID, Summary: "Create the result", RecordRevision: "fixture", State: goal.GoalBlocked},
	}
	if err = r.publishCodeGoals(t.Context(), goal.State{Goals: goals}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.journal().Get(t.Context(), "goal-result:"+codingID(bad.ID, string(goal.GoalBlocked), "fixture")); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("rejected summary was published: %v", err)
	}
	if _, err = r.journal().Get(t.Context(), "goal-result:"+codingID(good.ID, string(goal.GoalBlocked), "fixture")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkMethodDispatchUsesPreparedSourceRecord(t *testing.T) {
	r := controlRuntime(t)
	dev := workAgentFixture(t, r)
	if _, err := r.agents.Prepare(t.Context(), "pinned-work", dev); err != nil {
		t.Fatal(err)
	}
	if err := r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "dev", WorkMethod: `{"schema":1,"preset":"dev","workers":{"dispatch":false},"handoff":{"owns":["code"]}}`}, r.person); err != nil {
		t.Fatal(err)
	}
	origin := dispatch.Origin{Person: r.person, Agent: dev, Conversation: "main:" + dev, Turn: "pinned-work"}
	assignment, err := r.assignCoding(t.Context(), origin, "pinned-task", "worker_start", "/fixture", json.RawMessage(`{}`))
	if err != nil || assignment.Owner != dev {
		t.Fatalf("prepared dispatch record was replaced mid-turn: %+v %v", assignment, err)
	}
	origin.Turn = "next-work"
	if _, err = r.assignCoding(t.Context(), origin, "next-task", "worker_start", "/fixture", json.RawMessage(`{}`)); !errors.Is(err, errDevUnavailable) {
		t.Fatalf("new turn ignored disabled dispatch record: %v", err)
	}
}

func TestDelegatedGoalWorkerRetainsRequesterBinding(t *testing.T) {
	r := runRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "goal-worker-request", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "app"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the result"})
	if err != nil {
		t.Fatal(err)
	}
	origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:" + input.ID}
	a, err := r.assignCoding(t.Context(), origin, "delegated-goal", "dispatch_code_goal", "fixture", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	g := goal.Goal{ID: a.ID, Person: r.person, Agent: a.Owner, Conversation: a.Conversation, Turn: "coding:" + a.ID}
	record := dispatch.Record{Origin: goalOrigin(g), Owner: a.Owner, Manager: a.Conversation, Goal: true}
	delivery, restored, err := r.dispatchRecoveryView(t.Context(), record)
	if err != nil {
		t.Fatalf("goal worker lost requester: %v", err)
	}
	if delivery.agent != r.agent || delivery.conversation != r.conversation || restored.ID != input.ID {
		t.Fatal("wrong requester", restored.ID)
	}
	owner := newConversationView(r.router.root, a.Owner, a.Conversation)
	owner.codingDelivery = delivery
	if !owner.workerConversation(r) || !owner.workerConversation(owner) {
		t.Fatal("requester or manager cannot access worker")
	}
	foreign := newConversationView(r.router.root, r.agent, "unrelated")
	if owner.workerConversation(foreign) {
		t.Fatal("unrelated conversation acquired worker")
	}
	probe := &goalWorkerProbe{workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "goal-thread", TurnID: "worker-turn", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Active}}}
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "result.txt"), []byte("worker result"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Match the installed Core layout before applying its sandbox masks.
	for _, part := range []string{"system/bin", "run", "secrets", "backups", "staging", "materials", "versions", "derived", "canon"} {
		if err = os.MkdirAll(filepath.Join(r.root, part), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(executable, filepath.Join(r.root, "system/bin/lina")); err != nil {
		t.Fatal(err)
	}
	_, unbind := r.bindDispatchedWorker(record, probe, probe.snapshot.Thread, "Create the result", workdir)
	defer unbind()
	for _, view := range []*conversationRuntime{r, owner, foreign} {
		for _, name := range []string{"worker_snapshot", "worker_read_file", "worker_input"} {
			args := []byte(`{"session":"goal-thread"}`)
			if name == "worker_read_file" {
				args = []byte(`{"session":"goal-thread","path":"result.txt"}`)
			}
			if name == "worker_input" {
				args, _ = json.Marshal(workerArgs{Session: "goal-thread", Turn: "worker-turn", Generation: 1, Binding: 1, Text: "Continue"})
			}
			in, err := workerSchema(name, args)
			if err != nil {
				t.Fatal(err)
			}
			result, err := view.workerOperationWithOwner(t.Context(), name, "current-turn", input, in, true)
			want := tools.Applied
			if view == foreign {
				want = tools.Failed
			}
			if err != nil || result.State != want {
				t.Fatalf("%s for %s: %+v %v", name, view.conversation, result, err)
			}
			if name == "worker_read_file" && want == tools.Applied && !strings.Contains(string(result.Body), "worker result") {
				t.Fatal("worker file content missing")
			}
		}
	}
	if probe.inputs != 2 {
		t.Fatalf("control calls=%d", probe.inputs)
	}
	probe.snapshot.State = worker.Idle
	if err := r.ownerWorkerControl(t.Context(), clientsrv.Session{PersonID: r.person}, "session:goal-thread", &r.conversation, "idle-steer", "Continue"); err != nil {
		t.Fatal("idle follow-up did not record refusal", err)
	}
	messages, err := r.work.SteeringMessages(t.Context(), "codex:goal-thread")
	if err != nil || len(messages) != 3 || messages[2].State != "refused" || probe.inputs != 2 {
		t.Fatal("idle steering was not refused", messages, probe.inputs, err)
	}
	probe.snapshot.State = worker.Active
	cancelled := 0
	r.workers.sessions[probe.snapshot.Thread.ID].cancelTask = func(context.Context) error { cancelled++; return nil }
	stopped, err := r.stopAllRuns(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{MessageID: "stop-goal", Payload: protocol.MultiTaskControl{Action: "stop_all", ConversationID: &r.conversation}})
	if err != nil {
		t.Fatalf("requester could not stop dispatched worker: %v", err)
	}
	count := stopped.Payload.(protocol.MultiTaskControl).Count
	if cancelled != 1 || probe.interrupts != 1 || count == nil || *count != "1" {
		t.Fatalf("cancels=%d interrupts=%d count=%v", cancelled, probe.interrupts, count)
	}
	record.Origin.Conversation = "unrelated"
	if _, _, err := r.dispatchRecoveryView(t.Context(), record); err == nil {
		t.Fatal("mismatched assignment accepted")
	}
}

// Dispatched sessions can steer an active turn without starting a new one.
type goalWorkerProbe struct{ workerProbe }

func (p *goalWorkerProbe) Steer(ctx context.Context, thread worker.Thread, input worker.Input) error {
	return p.Input(ctx, thread, input)
}
