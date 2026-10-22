// SPDX-License-Identifier: Apache-2.0
package state

import (
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

// ToolCall is ephemeral presentation, never a conversation block or model item.
// Anchor fixes its position at first observation; finishing cannot reorder it.
type ToolCall struct {
	ID, Topic, Anchor, Name, Target, Summary string
	Turn                                     uint64
	Status                                   protocol.ToolActivityStatus
	Outcome                                  protocol.ToolActivityOutcome
}
type ToolEvent struct {
	Epoch, Topic string
	Cursor       uint64
	Call         ToolCall
}

func (ToolEvent) isMsg() {}

func (s *State) toolEvent(event ToolEvent) {
	call := event.Call
	call.Topic, call.Turn = event.Topic, s.ToolTurn
	call.Name = strings.Join(strings.Fields(sanitize.Display(call.Name)), " ")
	call.Target = strings.Join(strings.Fields(sanitize.Display(call.Target)), " ")
	call.Summary = sanitize.Display(call.Summary)
	if call.ID == "" || call.Name == "" {
		return
	}
	at := slices.IndexFunc(s.ToolCalls, func(old ToolCall) bool { return old.Topic == call.Topic && old.Turn == call.Turn && old.ID == call.ID })
	if at >= 0 {
		old := s.ToolCalls[at]
		// A repeated start cannot reopen a completed call or change its identity.
		if old.Status == protocol.ToolActivityStatusFinished {
			return
		}
		call.Anchor, call.Name = old.Anchor, old.Name
		if call.Target == "" {
			call.Target = old.Target
		}
		if call.Summary == "" {
			call.Summary = old.Summary
		}
		s.ToolCalls[at] = call
		return
	}
	for i := len(s.Blocks) - 1; i >= 0; i-- {
		b := s.Blocks[i]
		if b.Topic == event.Topic && b.Kind != protocol.KindTurnState && b.Kind != protocol.KindToolActivity {
			call.Anchor = b.ID
			break
		}
	}
	s.ToolCalls = append(s.ToolCalls, call)
}
