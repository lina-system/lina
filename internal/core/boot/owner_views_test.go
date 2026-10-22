// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
	"time"
)

func TestProgressOverviewWiredToCore(t *testing.T) {
	r := controlRuntime(t)
	if _, ok := r.identity.TopicCapabilities["progress"]; !ok {
		t.Fatal("progress capability missing")
	}
	task := work.Task{ID: "hidden-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Prepare workshop", Done: "materials verified"}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "hidden-run", TaskID: task.ID, Generation: 1, Worker: "Codex", ThreadID: "hidden-thread", TurnID: "hidden-turn", NodeID: r.main}); err != nil {
		t.Fatal(err)
	}
	mode := "overview"
	result, err := r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressGet{View: &mode}})
	if err != nil {
		t.Fatal(err)
	}
	p := result.Payload.(protocol.Progress)
	if p.Rows == nil || len(*p.Rows) != 1 || (*p.Rows)[0].Line.Name != task.Title || (*p.Rows)[0].Line.Stage != "unknown" || (*p.Rows)[0].Line.Percent != nil {
		t.Fatalf("unobserved progress: %+v", p)
	}
	if _, err = r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: "stranger"}, protocol.Envelope{Payload: protocol.ProgressGet{View: &mode}}); err == nil {
		t.Fatal("unauthorized progress")
	}
}

func TestProgressGroupsLiveSessionsAndKeepsTaskOutcomeUnknown(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	r.workers = &workerRuntime{sessions: map[string]*workerSession{}}
	t.Cleanup(func() { r.workers = nil })
	for _, id := range []string{"one", "two"} {
		snap := worker.ThreadSnapshot{Thread: worker.Thread{ID: id, TaskID: "same-request"}, State: worker.Idle, Output: "unverified worker output"}
		r.workers.sessions[id] = &workerSession{adapter: &workerProbe{snapshot: snap}, origin: r, thread: snap.Thread}
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows == nil || len(*p.Rows) != 1 {
		t.Fatalf("worker sessions became duplicate work lines: %+v", p)
	}
	if v := (*p.Rows)[0].Line; v.JustFinished != "unknown" || v.Stage != "unknown" || v.Percent != nil {
		t.Fatalf("worker idle became plan outcome: %+v", v)
	}
}

func TestFullWaitingChangesRefreshBeyondHomeLimit(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	put := func(tx *canon.Tx, id string) error {
		raw, _ := json.Marshal(authority.Approval{ID: id, Epoch: r.epoch, Person: r.person, Agent: r.agent, State: authority.Pending, What: "Decide " + id, Choices: []authority.Choice{authority.Once}})
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", id, id, "fixture", raw)
		return err
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, id := range []string{"a1", "a2", "a3", "a4", "a5", "a6"} {
			if err := put(tx, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() clientsrv.TopicStart {
		t.Helper()
		if err := r.publishOwnerViews(t.Context()); err != nil {
			t.Fatal(err)
		}
		start, sub, err := r.topics.Subscribe("progress", nil)
		if err != nil {
			t.Fatal(err)
		}
		sub.Close()
		return start
	}
	first := snapshot()
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), `UPDATE authority_approvals SET record=json_set(record,'$.State','approved') WHERE approval_id='a6'`); err != nil {
			return err
		}
		return put(tx, "a7")
	}); err != nil {
		t.Fatal(err)
	}
	second := snapshot()
	if first.Snapshot.Cursor == second.Snapshot.Cursor {
		t.Fatal("resolved row beyond Home limit did not invalidate the full waiting view")
	}
	names, _ := r.ownerAgentNames(t.Context())
	list, err := r.ownerWaiting(t.Context(), names, waiting.Options{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 6 || list.Entries[5].Ref.ID != "a7" {
		t.Fatalf("stale full list: %+v", list)
	}
}
