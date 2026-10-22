// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"encoding/json"

	"github.com/thisisjun786/lina/kit/protocol"
)

var TurnActivityCapability = protocol.Capability{Name: "turn_activity", Version: "1.0"}

// legacyActivity is owned by one connection's serialized topic writer. Earlier
// strict receivers cannot read new fields. Cursor-only turn states preserve the
// sequence of tool events without creating visible rows or resetting old timers.
type legacyActivity struct{ active map[string]bool }

func (p *legacyActivity) turn(state protocol.TurnState, topic string, snapshot bool) protocol.TurnState {
	state.Phase, state.ToolName = nil, nil
	if state.Active != nil && topic != "" {
		if p.active == nil {
			p.active = map[string]bool{}
		}
		active := *state.Active
		if !snapshot && active && p.active[topic] {
			state.Active = nil
		}
		p.active[topic] = active
	}
	return state
}
func (p *legacyActivity) snapshot(snapshot protocol.Snapshot) protocol.Snapshot {
	delete(p.active, snapshot.Topic)
	items := make([]protocol.StreamItem, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		if item.Kind == protocol.KindToolActivity {
			continue
		}
		if item.Kind == protocol.KindTurnState {
			var state protocol.TurnState
			// The strict producer codec already validated these payload bytes.
			if json.Unmarshal(item.Payload, &state) == nil {
				item.Payload, _ = json.Marshal(p.turn(state, snapshot.Topic, true))
			}
		}
		items = append(items, item)
	}
	snapshot.Items = items
	return snapshot
}
func (p *legacyActivity) project(envelope protocol.Envelope) protocol.Envelope {
	switch payload := envelope.Payload.(type) {
	case protocol.TurnState:
		topic := ""
		if payload.Stream != nil {
			topic = payload.Stream.Topic
		}
		envelope.Payload = p.turn(payload, topic, false)
	case protocol.ToolActivity:
		envelope.Kind = protocol.KindTurnState
		envelope.Payload = protocol.TurnState{Stream: payload.Stream, AgentID: payload.AgentID}
	case protocol.Snapshot:
		envelope.Payload = p.snapshot(payload)
	case protocol.History:
		if payload.Snapshot != nil {
			snapshot := p.snapshot(*payload.Snapshot)
			payload.Snapshot = &snapshot
		}
		envelope.Payload = payload
	}
	return envelope
}
