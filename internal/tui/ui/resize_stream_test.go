// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationRapidResizeStreamMatchesFresh(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := tuiterm.NewRecorder(100, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 100, 32, TrueColor)
	defer renderer.close()
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m.conversation.id = "fixture"
	topic := "conversation:fixture"
	history := []state.Block{}
	for i := 0; i < 4; i++ {
		history = append(history, state.Block{ID: fmt.Sprint(i), Text: fmt.Sprintf("reply-%d", i), Revision: 1, Done: true, Speaker: protocol.ConversationSpeakerAgent})
	}
	apply := func(msg state.Msg) { m.Update(shellData{Generation: m.generation, Message: msg}) }
	apply(state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Cursor: 1, Items: history})
	m.Update(tea.PasteMsg{Content: "초안"})
	show := func() {
		t.Helper()
		if err := renderer.show(m.View(), tea.WindowSizeMsg{Width: m.size.Width, Height: m.size.Height}); err != nil {
			t.Fatal(err)
		}
	}
	show()
	if _, err := output.Wait(ctx, func(s tuiterm.Snapshot) bool { return s.AltScreen && s.CursorY == 30 && s.CursorX == 6 }); err != nil {
		t.Fatal(err)
	}

	// Both physical resizes finish before the queued input notifications. A
	// small frame and a streaming update can still be painted before the wide
	// notification reaches the connection owner. Wait on observable writes,
	// never on elapsed time, to hold this otherwise scheduling-dependent order.
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 120, Height: 40}} {
		if err := output.Resize(size.Width, size.Height); err != nil {
			t.Fatal(err)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	show()
	if _, err := output.Wait(ctx, func(s tuiterm.Snapshot) bool { return s.CursorY == 10 && s.CursorX == 6 }); err != nil {
		t.Fatal(err)
	}

	if m.content.Draft.Text != "초안" {
		t.Fatal("resize changed draft")
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m.Update(tea.PasteMsg{Content: "next"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	sent := m.content.Blocks[len(m.content.Blocks)-1]
	apply(state.SendResult{Gen: m.content.Gen, Key: sent.ID})
	apply(state.ItemDone{Epoch: "1", Topic: topic, Cursor: 2, Block: state.Block{ID: "input", EchoKey: sent.ID, Text: sent.Text, Speaker: protocol.ConversationSpeakerPerson, Revision: 1, Done: true}})
	apply(state.Delta{Epoch: "1", Topic: topic, ID: "stream", Cursor: 3, Revision: 1, Text: "stream-411"})
	apply(state.Turn{Active: true})
	show()
	if _, err := output.Wait(ctx, func(s tuiterm.Snapshot) bool { return s.CursorY == 10 && s.CursorX == 2 }); err != nil {
		t.Fatal(err)
	}

	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	show()
	check := func(stage string) {
		t.Helper()
		m.Update(tea.PasteMsg{Content: "x"})
		show()
		fresh := conversationShell()
		fresh.conversation.id = "fixture"
		fresh.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		fresh.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Cursor: m.content.Topics[topic].Cursor, Items: m.content.Blocks}})
		fresh.Update(tea.PasteMsg{Content: m.content.Draft.Text})
		expected, err := tuiterm.NewRecorder(120, 40)
		if err != nil {
			t.Fatal(err)
		}
		defer expected.Close()
		other := startTerminalRenderer(ctx, rawTerminalWriter{expected}, 120, 40, TrueColor)
		defer other.close()
		v := fresh.View()
		if err := other.show(v, tea.WindowSizeMsg{Width: 120, Height: 40}); err != nil {
			t.Fatal(err)
		}
		settled := func(s tuiterm.Snapshot) bool {
			return s.AltScreen && !s.CursorHidden && s.CursorX == v.Cursor.X && s.CursorY == v.Cursor.Y
		}
		want, err := expected.Wait(ctx, settled)
		if err != nil {
			t.Fatal(err)
		}
		got, err := output.Wait(ctx, settled)
		if err != nil {
			t.Fatal(err)
		}
		var mismatched []int
		for y := range want.Cells {
			if !reflect.DeepEqual(got.Cells[y], want.Cells[y]) {
				mismatched = append(mismatched, y)
			}
		}

		if len(mismatched) > 0 {
			t.Errorf("%s: rows %v differ from fresh client", stage, mismatched)
		} else {
			t.Logf("%s: all %d rows and cursor match fresh client", stage, len(want.Cells))
		}
	}
	check("streaming")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.content.Stopping {
		t.Fatal("interrupt was not submitted")
	}
	apply(state.ItemDone{Epoch: "1", Topic: topic, Cursor: 4, Block: state.Block{ID: "stream", Revision: 2, Done: true, Text: "interrupted"}})
	apply(state.Turn{Active: false})
	check("interrupted")
	saved := append([]state.Block(nil), m.content.Blocks...)
	m.Update(shellBegin{Generation: 2})
	apply(state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Cursor: 4, Items: saved})
	m.agentDiscoveryDone()
	m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
	check("reconnected")
}
