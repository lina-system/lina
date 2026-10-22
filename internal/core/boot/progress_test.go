// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestWorkerLanesReadsLiveCoreWorkAndChecksPerson(t *testing.T) {
	r := controlRuntime(t)
	op, ok := r.identity.Operations[protocol.KindProgressGet]
	if !ok || op.Capability.Name != "worker_lanes" {
		t.Fatal("worker lanes not wired into Core")
	}
	task := work.Task{ID: "live-task", AgentID: r.agent, PersonID: r.person, ConversationID: r.conversation, Title: "Create report", Done: "report exists"}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	run := work.Run{ID: "live-run", TaskID: task.ID, Generation: 1, Worker: "Codex", ThreadID: "thread", TurnID: "turn", NodeID: "local"}
	if err := r.work.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	read := func() protocol.WorkerLane {
		t.Helper()
		result, err := op.Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressGet{}})
		if err != nil {
			t.Fatal(err)
		}
		p := result.Payload.(protocol.Progress)
		if p.Lanes == nil || len(*p.Lanes) != 1 {
			t.Fatalf("missing live lane %+v", p)
		}
		return (*p.Lanes)[0]
	}
	if lane := read(); lane.LaneID != run.ID || lane.State != "working" {
		t.Fatalf("wrong lane %+v", lane)
	}
	if err := r.work.Finish(t.Context(), task.ID, run.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	if lane := read(); lane.State == "done" || lane.State == "working" {
		t.Fatalf("refresh faked success or retained stale execution: %+v", lane)
	}
	if _, err := op.Handle(t.Context(), clientsrv.Session{PersonID: "other"}, protocol.Envelope{Payload: protocol.ProgressGet{}}); err == nil {
		t.Fatal("other person read worker lanes")
	}
}
