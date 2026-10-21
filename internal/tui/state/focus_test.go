// SPDX-License-Identifier: Apache-2.0

package state

import "testing"

func TestAgentFocusDropsOldConversationAndSendState(t *testing.T) {
	s := State{Gen: 3, Epoch: "7", Draft: Draft{Text: "draft"}, Busy: true, Stopping: true, TurnActive: true, Topics: map[string]Topic{"conversation:old": {Cursor: 8}, "waiting": {Cursor: 5}}, Blocks: []Block{{ID: "old", Topic: "conversation:old"}, {ID: "echo", Local: true}, {ID: "wait", Topic: "waiting"}}}
	next, commands := Reduce(s, FocusConversation{})
	if len(commands) != 0 || next.Busy || next.Stopping || next.TurnActive || next.Gen <= s.Gen || len(next.Blocks) != 1 || next.Blocks[0].ID != "wait" || len(next.Topics) != 1 || next.Epoch != "7" || next.Draft.Text != "draft" {
		t.Fatal("focus retained old lane or discarded independent display state")
	}
	if len(s.Blocks) != 3 || len(s.Topics) != 2 {
		t.Fatal("focus mutated prior state")
	}
}
