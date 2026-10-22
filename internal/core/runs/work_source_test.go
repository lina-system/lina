// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"strings"
	"testing"
	"time"
)

func TestLiveTaskList(t *testing.T) {
	db, _ := listDB(t)
	s := work.New(db)
	task := work.Task{ID: "task-1", AgentID: "agent-1", ConversationID: "conversation-1", Title: "Produce report", Done: "report exists", ArtifactRoots: []string{"/work/task-1"}}
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	run := work.Run{ID: "run-1", TaskID: task.ID, Generation: 1, Worker: "fixture", ThreadID: "thread-1", TurnID: "turn-1", NodeID: "node-1"}
	if err := s.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	list := runs.NewRunningList(db, map[string]runs.ListSource{"work": runs.WorkListSource{Person: "person-1"}})
	snapshot, err := list.Build(ctx, "person-1")
	if err != nil || snapshot.Working != 1 || len(snapshot.Entries) != 1 {
		t.Fatalf("working: %+v %v", snapshot, err)
	}
	snapshot, err = list.Build(ctx, "other")
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("other person: %+v %v", snapshot, err)
	}
	if err = s.Recover(ctx, "epoch-2"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(ctx, "person-1")
	if err != nil || snapshot.Entries[0].View.State != "unknown" {
		t.Fatalf("unknown: %+v %v", snapshot, err)
	}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	r := work.Receipt{SchemaVersion: 1, RelationshipID: task.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: "/work/task-1/report.txt", SHA256: strings.Repeat("a", 64)}}}
	r.RevisionHash = work.ManifestHash(r.Manifest)
	r.EventID = work.ReceiptID(r)
	r.Provenance = []work.Provenance{{ArtifactID: "artifact-1", TaskID: task.ID, NodeID: run.NodeID, Path: r.Manifest[0].Path, SHA256: r.Manifest[0].SHA256, ObservedAt: now}}
	if err = s.Submit(ctx, "run-1", r); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err = s.Judge(ctx, "task-1", r.EventID, "verified", "review-1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(ctx, "person-1")
	if err != nil || snapshot.Landed != 1 || snapshot.Entries[0].View.State != "done" {
		t.Fatalf("done: %+v %v", snapshot, err)
	}
	oldRef := snapshot.Entries[0].Ref
	if err = list.Dismiss(ctx, "person-1", oldRef); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(ctx, "person-1")
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("dismissed: %+v %v", snapshot, err)
	}
	// Keep the first-generation row identity rather than reconstructing it.
	next := run
	next.ID = "run-2"
	next.Generation = 2
	next.TurnID = "turn-2"
	if err = s.Start(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(ctx, "task-1", next.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(ctx, "person-1")
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].View.State != "failed" {
		t.Fatalf("previous dismissal hid a later result: %+v %v", snapshot, err)
	}
	if err = list.Dismiss(ctx, "person-1", oldRef); err == nil {
		t.Fatal("stale row dismissed current generation")
	}
}
