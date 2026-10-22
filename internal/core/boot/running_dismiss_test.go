// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
)

func TestDismissedRunningRowLeavesPublishedSnapshot(t *testing.T) {
	r := runRuntime(t)
	b := runs.Brief{Goal: "inspect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	id, _, err := r.background.service.Start(t.Context(), r.runOrigin("dismiss"), b)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.background.service.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = r.publishRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, live, err := r.topics.Subscribe("running", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if before.Snapshot == nil || len(before.Snapshot.Items) != 1 {
		t.Fatal("missing landed row")
	}
	if err = r.background.list.Dismiss(t.Context(), r.person, runs.ListRef{Source: "run", ID: id}); err != nil {
		t.Fatal(err)
	}
	if err = r.publishRunning(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, sub, err := r.topics.Subscribe("running", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if after.Snapshot == nil || len(after.Snapshot.Items) != 0 {
		t.Fatalf("dismissed row retained: %+v", after.Snapshot)
	}
	frame, err := live.Next(t.Context())
	if err != nil || frame.Event.Key != id {
		t.Fatalf("live removal missing: %+v %v", frame, err)
	}
}
