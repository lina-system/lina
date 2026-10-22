// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSwitchOwnerAdmission(t *testing.T) {
	r, session, agent, _ := managementFixture(t)
	r.work = work.New(r.db)
	task := work.Task{ID: "task", PersonID: session.PersonID, AgentID: agent, ConversationID: "conversation", Title: "Report", Done: "Verified", ExternalControl: true}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	tasks := []string{task.ID}
	snapshot := strings.Repeat("a", 64)
	request := protocol.WorkSwitchRequest{Operation: "approve_switch", SwitchID: "switch", Tasks: &tasks, Snapshot: &snapshot}
	if _, err := r.Work(t.Context(), session, request, "approval"); err == nil {
		t.Fatal("worker approved switch")
	}
	session.CanDecide = true
	foreign := session
	foreign.PersonID = "other"
	if _, err := r.Work(t.Context(), foreign, request, "approval"); err == nil {
		t.Fatal("foreign person approved switch")
	}
	v, err := r.Work(t.Context(), session, request, "approval")
	if err != nil || v.State != "approved" || v.Approval != "approval" {
		t.Fatalf("approval: %+v %v", v, err)
	}
	if _, err = r.Work(t.Context(), session, request, "different-message"); err == nil {
		t.Fatal("approval was overwritten")
	}
}

func TestSwitchBlocksLiveWorkerBeforeLaunchAndSteering(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.work = work.New(r.db)
	r.workers = &workerRuntime{sessions: map[string]*workerSession{}}
	dir := t.TempDir()
	task := work.Task{ID: "external", PersonID: r.person, AgentID: agent, ConversationID: "conversation", Title: "Work", Done: "Result", ExternalControl: true, ArtifactRoots: []string{dir}}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(workerArgs{Workdir: dir, TaskPacket: "do work"})
	in, err := workerSchema("worker_start", raw)
	if err != nil {
		t.Fatal(err)
	}
	// No activity registry or installed executable: any attempted launch would
	// fail this test before the expected, durable admission refusal.
	result, err := r.workerOperation(t.Context(), "worker_start", "turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Failed || !strings.Contains(string(result.Body), work.ErrControlHeld.Error()) {
		t.Fatal("dispatch bypassed switch gate", result, err)
	}
	sw := work.Switch{ID: "switch", Owner: r.person, Approval: "approval", Snapshot: strings.Repeat("a", 64), Tasks: []string{task.ID}}
	if err = r.work.ApproveSwitch(t.Context(), sw); err != nil {
		t.Fatal(err)
	}
	if err = r.work.ActivateSwitch(t.Context(), sw.ID, r.person, "proof"); err != nil {
		t.Fatal(err)
	}
	if err = r.work.ReserveWorker(t.Context(), "worker", dir); err != nil {
		t.Fatal(err)
	}
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "thread", TurnID: "worker-turn", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Active}}
	r.workers.sessions["thread"] = &workerSession{adapter: probe, origin: r, thread: probe.snapshot.Thread, workdir: dir, reservation: "worker", reservationTurn: "worker-turn", active: map[string]lease.Lease{}}
	if err = r.work.RequestRollback(t.Context(), sw.ID, r.person, "failed"); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(workerArgs{Session: "thread", Turn: "worker-turn", Generation: 1, Binding: 1, Text: "steer"})
	in, err = workerSchema("worker_input", raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err = r.workerOperation(t.Context(), "worker_input", "turn", ledger.Event{}, in)
	if err != nil || result.State != tools.Failed || probe.inputs != 0 {
		t.Fatal("steering bypassed rollback gate", result, err)
	}
}
