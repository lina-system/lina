// SPDX-License-Identifier: Apache-2.0

package state

import (
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestActivityUsesServerClockAndCompletesOnce(t *testing.T) {
	server := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := server.Add(4 * time.Hour)
	topic := "conversation:fixture"
	s, _ := Reduce(State{}, Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Cursor: 1, ServerTime: server, ClientTime: client})
	active := ItemDone{Epoch: "1", Topic: topic, Cursor: 2, Block: Block{ID: "turn", Kind: protocol.KindTurnState, Revision: 1, TurnKnown: true, TurnActive: true, TurnStarted: server}}
	s, _ = Reduce(s, active)
	s, _ = Reduce(s, Tick{Now: client.Add(2 * time.Second)})
	if got := s.ServerNow.Sub(s.Activity.Started); got != 2*time.Second {
		t.Fatalf("server duration=%s", got)
	}
	s, _ = Reduce(s, Delta{Epoch: "1", Topic: topic, Cursor: 3, Revision: 1, ID: "reply", Text: "partial"})
	if !s.Activity.Replying || s.Activity.ReplyID != "reply" {
		t.Fatal("accepted delta did not mark replying")
	}
	s, _ = Reduce(s, Interrupt{})
	before := s
	s, _ = Reduce(s, active)
	if !reflect.DeepEqual(s, before) {
		t.Fatal("replayed active frame changed activity")
	}
	s, _ = Reduce(s, ItemDone{Epoch: "1", Topic: topic, Cursor: 4, Block: Block{ID: "reply", Kind: protocol.KindItemDone, Speaker: protocol.ConversationSpeakerAgent, Revision: 2, Text: "reply"}})
	if !s.Stopping {
		t.Fatal("item completion cleared stopping before turn completion")
	}
	idle := ItemDone{Epoch: "1", Topic: topic, Cursor: 5, Block: Block{ID: "turn", Kind: protocol.KindTurnState, Revision: 2, TurnKnown: true, TurnAt: server.Add(12 * time.Second)}}
	s, _ = Reduce(s, idle)
	if s.TurnActive || s.Stopping || s.Worked["reply"] != 12*time.Second {
		t.Fatalf("completion: %+v", s)
	}
	before = s
	s, _ = Reduce(s, idle)
	if !reflect.DeepEqual(s, before) {
		t.Fatal("completion replay changed display")
	}
	// Existing display metadata survives a same-session snapshot. Fresh clients
	// do not fabricate it from completed history, and revocation removes it.
	snapshot := Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Cursor: 6, Items: s.Blocks, ServerTime: server.Add(20 * time.Second), ClientTime: client.Add(20 * time.Second)}
	s, _ = Reduce(s, snapshot)
	if s.Worked["reply"] != 12*time.Second {
		t.Fatal("snapshot changed completion duration")
	}
	fresh, _ := Reduce(State{}, snapshot)
	if len(fresh.Worked) != 0 {
		t.Fatal("completion duration entered restored history")
	}
	snapshot.Revocation = "2"
	snapshot.Items = nil
	s, _ = Reduce(s, snapshot)
	if len(s.Worked) != 0 {
		t.Fatal("revocation retained display metadata")
	}
}

func TestActivitySnapshotAndSendFailure(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, _ := Reduce(State{}, Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: 3, Items: []Block{
		{ID: "turn", Kind: protocol.KindTurnState, TurnKnown: true, TurnActive: true, TurnStarted: started, Done: true},
		{ID: "reply", Kind: protocol.KindOutputDelta, Text: "partial"},
	}})
	if !s.TurnActive || !s.Activity.Replying || !s.Activity.Started.Equal(started) {
		t.Fatalf("snapshot activity=%+v", s.Activity)
	}
	s, _ = Reduce(s, FocusConversation{})
	if s.TurnActive || !s.Activity.Started.IsZero() || len(s.Worked) > 0 {
		t.Fatal("focus retained prior activity")
	}
	s, _ = Reduce(s, EditDraft{Draft: Draft{Text: "draft"}})
	s, _ = Reduce(s, Send{Key: "pending"})
	if !s.Busy {
		t.Fatal("send did not become busy immediately")
	}
	s, _ = Reduce(s, SendResult{Gen: s.Gen, Key: "pending", Failed: true})
	if s.Busy || s.TurnActive || s.Draft.Text != "draft" || len(s.Worked) > 0 {
		t.Fatal("failed send retained activity or duration")
	}
}

func TestAuthoritativeEmptyConversationStopsActivity(t *testing.T) {
	s, _ := Reduce(State{}, Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: 1, Items: []Block{{ID: "turn", Kind: protocol.KindTurnState, TurnKnown: true, TurnActive: true}}})
	s, _ = Reduce(s, Snapshot{Epoch: "1", Revocation: "1", Topic: "results:fixture", Cursor: 1})
	if !s.TurnActive {
		t.Fatal("unrelated snapshot cleared activity")
	}
	s, _ = Reduce(s, Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: 2})
	if s.TurnActive || len(s.Worked) > 0 {
		t.Fatal("empty authoritative conversation retained activity")
	}
}
