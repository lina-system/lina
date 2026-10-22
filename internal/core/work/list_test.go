// SPDX-License-Identifier: Apache-2.0
package work

import (
	"github.com/thisisjun786/lina/internal/core/runs"
	"testing"
)

func TestLiveTaskList(t *testing.T) {
	s, db := testStore(t)
	startFixture(t, s)
	ctx := t.Context()
	list := runs.NewRunningList(db, map[string]runs.ListSource{"work": ListSource{Person: "person-1"}})
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
	r := receiptFixture()
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
	if err = list.Dismiss(ctx, "person-1", snapshot.Entries[0].Ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err = list.Build(ctx, "person-1")
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("dismissed: %+v %v", snapshot, err)
	}
}
