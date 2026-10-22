// SPDX-License-Identifier: Apache-2.0
package state

import (
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestWorkerLanesIdentityFoldsAndReplacement(t *testing.T) {
	a := protocol.WorkerLane{LaneID: "run-a", TaskID: "a", Worker: "Codex", Title: "title\x1b]52;c;ZXZpbA==\a", Details: []string{"result"}}
	b := protocol.WorkerLane{LaneID: "run-b", TaskID: "b", Worker: "Codex"}
	v := (WorkerLanes{}).Replace([]protocol.WorkerLane{a, b, a})
	if len(v.Rows) != 2 || v.Expanded["run-a"] || v.Expanded["run-b"] {
		t.Fatal("sessions combined, duplicate added or default unfolded")
	}
	old := v
	v = v.Move(1).Toggle()
	if !v.Expanded["run-b"] || old.Expanded["run-b"] {
		t.Fatal("toggle mutated previous state or wrong identity")
	}
	v = v.Replace([]protocol.WorkerLane{b, a})
	if v.Selected != "run-b" || !v.Expanded["run-b"] {
		t.Fatal("refresh lost identity")
	}
	a.LaneID = "run-new"
	v = v.Replace([]protocol.WorkerLane{a})
	if v.Selected != "run-new" || v.Expanded["run-new"] || v.Expanded["run-b"] {
		t.Fatal("new generation inherited old fold or retained removed session")
	}
	if strings.Contains(v.Rows[0].Title, "ZXZpbA") || strings.Contains(v.Rows[0].Title, "\x1b") {
		t.Fatal("escape survived")
	}
	a.Details[0] = "mutated"
	if v.Rows[0].Details[0] != "result" {
		t.Fatal("caller owns displayed details")
	}
}
