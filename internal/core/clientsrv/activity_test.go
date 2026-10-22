// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestLegacyActivityKeepsCursorAndTurnBoundaries(t *testing.T) {
	var legacy legacyActivity
	active := true
	phase := protocol.TurnPhaseTool
	name := "Reading skill"
	metadata := &protocol.StreamMetadata{Topic: "conversation:test", Key: "turn", Revision: "1", Cursor: protocol.StreamCursor{Epoch: "1", Seq: "1"}}
	event := protocol.Envelope{Kind: protocol.KindTurnState, Payload: protocol.TurnState{Active: &active, Phase: &phase, ToolName: &name, Stream: metadata}}
	first := legacy.project(event).Payload.(protocol.TurnState)
	if first.Active == nil || !*first.Active || first.Phase != nil || first.ToolName != nil || first.Stream != metadata {
		t.Fatalf("first legacy state: %+v", first)
	}
	next := legacy.project(event).Payload.(protocol.TurnState)
	if next.Active != nil {
		t.Fatal("phase reset old timer")
	}
	// Projection does not mutate the shared event seen by the new client.
	original := event.Payload.(protocol.TurnState)
	if original.Phase == nil || original.ToolName == nil || original.Active == nil {
		t.Fatal("shared payload changed")
	}
	event = protocol.Envelope{Kind: protocol.KindToolActivity, Payload: protocol.ToolActivity{Stream: metadata, CallID: "call", ToolName: name, Status: protocol.ToolActivityStatusStarted}}
	projected := legacy.project(event)
	state, ok := projected.Payload.(protocol.TurnState)
	if !ok || projected.Kind != protocol.KindTurnState || state.Stream != metadata || state.Active != nil {
		t.Fatalf("tool event did not consume cursor: %+v", projected)
	}
	active = false
	event = protocol.Envelope{Kind: protocol.KindTurnState, Payload: protocol.TurnState{Active: &active, Stream: metadata}}
	stopped := legacy.project(event).Payload.(protocol.TurnState)
	if stopped.Active == nil || *stopped.Active {
		t.Fatal("missing idle")
	}
	active = true
	if legacy.project(event).Payload.(protocol.TurnState).Active == nil {
		t.Fatal("next turn lost active boundary")
	}
}

func TestLegacyActivitySnapshotRetainsOnlyOldFields(t *testing.T) {
	var legacy legacyActivity
	raw := json.RawMessage(`{"active":true,"phase":"tool","tool_name":"Reading skill"}`)
	snapshot := protocol.Snapshot{Topic: "conversation:test", Cursor: protocol.StreamCursor{Epoch: "1", Seq: "7"}, Items: []protocol.StreamItem{
		{Kind: protocol.KindTurnState, Key: "turn", Revision: "6", Payload: raw},
		{Kind: protocol.KindToolActivity, Key: "call", Revision: "7", Payload: json.RawMessage(`{"call_id":"call","tool_name":"Reading skill","status":"started"}`)},
	}}
	projected := legacy.project(protocol.Envelope{Kind: protocol.KindSnapshot, Payload: snapshot}).Payload.(protocol.Snapshot)
	if len(projected.Items) != 1 || projected.Cursor != snapshot.Cursor || string(projected.Items[0].Payload) != `{"active":true}` {
		t.Fatalf("legacy snapshot: %+v", projected)
	}
	if string(snapshot.Items[0].Payload) != string(raw) || len(snapshot.Items) != 2 {
		t.Fatal("snapshot projection mutated shared data")
	}
	metadata := &protocol.StreamMetadata{Topic: snapshot.Topic}
	active := true
	if legacy.project(protocol.Envelope{Payload: protocol.TurnState{Active: &active, Stream: metadata}}).Payload.(protocol.TurnState).Active != nil {
		t.Fatal("replay after snapshot reset old timer")
	}
}
