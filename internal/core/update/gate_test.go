// SPDX-License-Identifier: Apache-2.0
package update

import "testing"

func TestGateRequiresEveryCell(t *testing.T) {
	if (Gate{}).Ready() {
		t.Fatal("missing observations opened the gate")
	}
	all := Gate{Idle: Pass, Candidate: Pass, Schema: Pass, Peers: Pass, Space: Pass, Generation: Pass}
	if !all.Ready() {
		t.Fatal("complete evidence refused")
	}
	for _, bad := range []Cell{"", Unknown, Fail, "unrecognized"} {
		for i := range 6 {
			gate := all
			cells := []*Cell{&gate.Idle, &gate.Candidate, &gate.Schema, &gate.Peers, &gate.Space, &gate.Generation}
			*cells[i] = bad
			if gate.Ready() {
				t.Fatalf("missing/failed cell %d opened gate: %q", i, bad)
			}
		}
	}
}
