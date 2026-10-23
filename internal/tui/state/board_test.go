// SPDX-License-Identifier: Apache-2.0
package state

import (
	"github.com/thisisjun786/lina/kit/protocol"
	"reflect"
	"testing"
)

func TestBoardPreservesCoreOrderAndReconcilesIdentities(t *testing.T) {
	running := []Block{
		{ID: "a", RunID: "a", RunState: protocol.RunningStateReadyForReview},
		{ID: "b", RunID: "b", RunState: protocol.RunningStateNeedsInput},
		{ID: "c", RunID: "c", RunState: protocol.RunningStateInProgress},
		{ID: "d", RunID: "d", RunState: protocol.RunningStateWaiting, RunReason: protocol.RunningReasonQuota},
		{ID: "e", RunID: "e", RunState: protocol.RunningStateFailed},
		{ID: "f", RunID: "f", RunState: protocol.RunningStateUnknown},
		{ID: "g", RunID: "g", RunState: protocol.RunningStateWaiting, RunReason: protocol.RunningReasonPerson},
		{ID: "h", RunID: "h", RunState: protocol.RunningStateStopped},
		{ID: "i", RunID: "i", RunState: protocol.RunningStateDone},
	}
	waiting := []protocol.WaitingRow{{Ref: protocol.WaitingRef{Source: "run", Key: "b"}}, {Ref: protocol.WaitingRef{Source: "outside", Key: "z"}}}
	board := (Board{}).Replace(running, waiting)
	var ids []string
	for _, row := range board.Rows {
		ids = append(ids, row.ID)
	}
	want := []string{"run:b", "waiting:outside:z", "run:g", "run:c", "run:d", "run:f", "run:a", "run:e", "run:h", "run:i"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("groups, order or duplicate run: %v", ids)
	}
	board = board.Move(3).Toggle()
	running[2].RunState = protocol.RunningStateDone
	board = board.Replace(running, waiting)
	if board.Selected != "run:c" || !board.Expanded["run:c"] {
		t.Fatal("regrouping changed selection or expansion")
	}
	board = board.Replace(nil, nil)
	if len(board.Rows) != 0 || board.Selected != "" || len(board.Expanded) != 0 {
		t.Fatal("removed identities survived replacement")
	}
}

func TestBoardWaitingTaskUsesCurrentGenerationReference(t *testing.T) {
	waiting := []protocol.WaitingRow{{Ref: protocol.WaitingRef{Source: "work", Key: "task:with:colons"}}}
	running := []Block{{ID: "current", RunID: "task:task:with:colons:7", RunState: protocol.RunningStateNeedsInput}}
	board := (Board{}).Replace(running, waiting)
	if len(board.Rows) != 1 || board.Rows[0].RunID != running[0].RunID || !board.Rows[0].CanNote() {
		t.Fatalf("waiting task lost generation-bound control identity: %+v", board.Rows)
	}
	board = board.Replace(nil, waiting)
	if len(board.Rows) != 1 || board.Rows[0].RunID != "" || board.Rows[0].CanStop() {
		t.Fatal("missing execution exposed a fabricated control reference")
	}
}
