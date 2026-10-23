// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
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

func TestWorkerLanesIncludesInstalledSessions(t *testing.T) {
	r := controlRuntime(t)
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "session", TurnID: "turn", TaskID: "task", ProcessGeneration: 1}, State: worker.Idle, Worker: "Codex", Version: "fixture", Output: "produced result"}}
	addWorkerFixtureSessions(t, r, map[string]*workerSession{"session": {adapter: probe, origin: r, thread: probe.snapshot.Thread}})
	result, err := r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressGet{}})
	if err != nil {
		t.Fatal(err)
	}
	lanes := result.Payload.(protocol.Progress).Lanes
	if lanes == nil || len(*lanes) != 1 || (*lanes)[0].ThreadID != "session" || (*lanes)[0].State != "idle" {
		t.Fatalf("installed worker absent from lanes: %+v", lanes)
	}
}

func TestSessionLanesPageFiltersAuthorityBeforeAdapters(t *testing.T) {
	r := controlRuntime(t)
	sessions := map[string]*workerSession{}
	for _, id := range []string{"a", "b"} {
		snap := worker.ThreadSnapshot{Thread: worker.Thread{ID: id}, Worker: "codex", State: worker.Active}
		sessions[id] = &workerSession{adapter: &workerProbe{snapshot: snap}, origin: r, thread: snap.Thread}
	}
	// A nil adapter would panic if an invisible origin were inspected.
	sessions["foreign"] = &workerSession{origin: &conversationRuntime{person: "other", agent: r.agent}}
	sessions["deleted"] = &workerSession{origin: &conversationRuntime{person: r.person, agent: "removed"}}
	addWorkerFixtureSessions(t, r, sessions)
	names := map[string]string{r.agent: "LINA"}
	rows, next := r.sessionLanes(t.Context(), r.person, "", names, 1)
	if len(rows) != 1 || rows[0].ThreadID != "a" || next == nil {
		t.Fatalf("first page: %+v %v", rows, next)
	}
	var cursor laneCursor
	if err := json.Unmarshal([]byte(*next), &cursor); err != nil {
		t.Fatal(err)
	}
	rows, next = r.sessionLanes(t.Context(), r.person, cursor.After, names, 1)
	if len(rows) != 1 || rows[0].ThreadID != "b" || next != nil {
		t.Fatalf("last page: %+v %v", rows, next)
	}
}
