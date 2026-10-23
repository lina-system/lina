// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestProgressPagerUsesCurrentRowsWithoutIdentity(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "progress")})
	m.Update(tea.PasteMsg{Content: "keep my draft"})
	frame := func(name, seq string, snapshot bool) state.Frame {
		var e protocol.Envelope
		if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &e); err != nil {
			t.Fatal(err)
		}
		rows := []protocol.ProgressRow{{Ref: "hidden-id", Line: protocol.ProgressLine{Name: name, Kind: "project", Stage: "unknown", Next: "unknown", JustFinished: "unknown", Waiting: "0", Depth: "0"}}}
		cursor := protocol.StreamCursor{Epoch: e.Epoch, Seq: json.Number(seq)}
		p := protocol.ProgressUpdate{Rows: &rows}
		if snapshot {
			raw, _ := json.Marshal(p)
			e.Payload = protocol.Snapshot{Topic: "progress", Cursor: cursor, RevocationEpoch: "0", Items: []protocol.StreamItem{{Kind: protocol.KindProgressUpdate, Key: "overview", Revision: json.Number(seq), Payload: raw}}}
		} else {
			e.Kind = protocol.KindProgressUpdate
			p.Stream = &protocol.StreamMetadata{Topic: "progress", Key: "overview", Cursor: cursor, Revision: json.Number(seq)}
			e.Payload = p
		}
		return streamFrame(t, e)
	}
	m.Update(shellData{Generation: 1, Message: frame("Workshop", "1", true)})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if !strings.Contains(m.conversation.pager.text, "Workshop") || !strings.Contains(m.conversation.pager.text, "unknown") || strings.Contains(m.conversation.pager.text, "hidden-id") {
		t.Fatalf("progress: %s", m.conversation.pager.text)
	}
	m.Update(shellData{Generation: 1, Message: frame("Updated workshop", "2", false)})
	if !strings.Contains(m.conversation.pager.text, "Updated workshop") || m.content.Draft.Text != "keep my draft" {
		t.Fatal("progress not live or lost draft")
	}
}

func TestOwnerCommandsAskCoreAndRetainWaitingOrder(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running", "progress", "waiting")})
	m.content.Blocks = []state.Block{{ID: "a-run", Topic: "running", RunID: "a-run", RunState: protocol.RunningStateWaiting}}
	for _, text := range []string{"/progress", "/waiting lightest", "/stop a-run", "/stop all", "/note a-run Read appendix"} {
		handled, cmd := m.ownerCommand(text)
		if !handled || cmd == nil {
			t.Fatalf("command lost: %s", text)
		}
		msg, ok := cmd().(shellManagement)
		if !ok {
			t.Fatal("not a Core request")
		}
		_, want, _ := protocol.OwnerCommand(text, "")
		gotRaw, _ := json.Marshal(msg.Request)
		wantRaw, _ := json.Marshal(want)
		if string(gotRaw) != string(wantRaw) {
			t.Fatalf("request changed: %s != %s", gotRaw, wantRaw)
		}
	}
	msg := m.waitingRequest()().(shellManagement)
	if p := msg.Request.(protocol.WaitingGet); p.Order == nil || *p.Order != "lightest" {
		t.Fatal("refresh discarded chosen order")
	}
	m.conversation.pager.waiting = true
	m.conversation.pager.open = true
	cmd := m.pagerKey(tea.KeyPressMsg{Code: 'o'}, "o")
	if p := cmd().(shellManagement).Request.(protocol.WaitingGet); p.Order == nil || *p.Order != "priority" {
		t.Fatal("order switch did not ask Core")
	}
}
