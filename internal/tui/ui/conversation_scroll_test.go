// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
)

func scrollShell() *shellModel {
	m := conversationShell()
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("row-%03d", i))
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: []state.Block{{ID: "long", Text: strings.Join(lines, "\n"), Done: true, Revision: 1}}}})
	m.View()
	return m
}

func conversationRows(m *shellModel) []string {
	view := m.View()
	p := m.conversationPlan().Conversation
	return strings.Split(view.Content, "\n")[p.Y : p.Y+p.Height]
}

func TestConversationWheelAcceleration(t *testing.T) {
	m := scrollShell()
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	wheel := func(button tea.MouseButton) { m.Update(tea.MouseWheelMsg{Button: button, X: 5, Y: 5}); m.View() }
	wheel(tea.MouseWheelUp)
	if m.conversation.scroll != 3 {
		t.Fatalf("first notch = %d rows, want 3", m.conversation.scroll)
	}
	now = now.Add(50 * time.Millisecond)
	wheel(tea.MouseWheelUp)
	if m.conversation.scroll != 9 {
		t.Fatalf("rapid repeat = %d, want 9", m.conversation.scroll)
	}
	now = now.Add(50 * time.Millisecond)
	wheel(tea.MouseWheelDown)
	if m.conversation.scroll != 6 {
		t.Fatalf("direction reset = %d, want 6", m.conversation.scroll)
	}
	now = now.Add(time.Second)
	wheel(tea.MouseWheelUp)
	if m.conversation.scroll != 9 {
		t.Fatalf("idle reset = %d, want 9", m.conversation.scroll)
	}
	for i := 0; i < 20; i++ {
		now = now.Add(time.Millisecond)
		wheel(tea.MouseWheelUp)
	}
	top := conversationRows(m)[0]
	if !strings.Contains(top, "row-000") {
		t.Fatalf("top clamp = %q", top)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	wheel(tea.MouseWheelDown)
	if m.conversation.scroll != 0 {
		t.Fatal("wheel down crossed bottom")
	}
}

func TestConversationScrollStaysPutForNewOutput(t *testing.T) {
	m := scrollShell()
	m.Update(tea.PasteMsg{Content: "keep draft"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	before := conversationRows(m)
	marker := design.Text(design.English, "conversation.new_messages")
	if strings.Contains(m.View().Content, marker) {
		t.Fatal("scrolling alone claims new output")
	}
	m.Update(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: "conversation:fixture", Cursor: 1, ID: "new", Revision: 1, Text: "arriving"}})
	if after := conversationRows(m); !reflect.DeepEqual(before, after) {
		t.Fatalf("stream moved viewport:\n%v\n%v", before, after)
	}
	if !strings.Contains(m.View().Content, marker) {
		t.Fatal("new output marker absent")
	}
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "conversation:fixture", Cursor: 2, Block: state.Block{ID: "new", Text: "arriving\ncomplete", Done: true, Revision: 2}}})
	if after := conversationRows(m); !reflect.DeepEqual(before, after) {
		t.Fatal("completion moved viewport")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if view := m.View().Content; !strings.Contains(view, "complete") || strings.Contains(view, marker) {
		t.Fatal("End did not resume following")
	}
	if m.content.Draft.Text != "keep draft" {
		t.Fatal("scroll edited draft")
	}
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "conversation:fixture", Cursor: 3, Block: state.Block{ID: "next", Text: "followed", Done: true, Revision: 1}}})
	if !strings.Contains(strings.Join(conversationRows(m), "\n"), "followed") {
		t.Fatal("latest did not follow new output")
	}
}

func TestConversationScrollHintAndOverlays(t *testing.T) {
	m := scrollShell()
	m.Update(tea.WindowSizeMsg{Width: 240, Height: 24})
	hint := design.Text(design.English, "conversation.scroll_hint")
	if !strings.Contains(m.View().Content, hint) {
		t.Fatal("overflow has no scroll hint")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	if strings.Contains(m.View().Content, hint) {
		t.Fatal("optional hint crowded narrow status")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m.View()
	before := m.conversation.scroll
	m.openPager("pager")
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: 5})
	if m.conversation.scroll != before {
		t.Fatal("wheel scrolled hidden conversation under pager")
	}
	m.conversation.pager.open = false
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 1, Y: 23})
	if m.conversation.scroll != before {
		t.Fatal("wheel outside transcript moved conversation")
	}
}

func TestConversationWheelIgnoresWebOverlay(t *testing.T) {
	for _, scrolled := range []bool{false, true} {
		for _, form := range []struct {
			name          string
			busy, editing bool
		}{
			{name: "status"},
			{name: "pending", busy: true},
			{name: "editing", editing: true},
		} {
			t.Run(fmt.Sprintf("scrolled=%t/%s", scrolled, form.name), func(t *testing.T) {
				m := scrollShell()
				if scrolled {
					m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5, Y: 5})
					m.View()
				}
				before := m.conversation.scroll
				viewport := m.conversation.viewport
				m.web = webView{open: true, busy: form.busy, editing: form.editing}
				for _, button := range []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown} {
					m.Update(tea.MouseWheelMsg{Button: button, X: 5, Y: 5})
					if m.conversation.scroll != before || !reflect.DeepEqual(m.conversation.viewport, viewport) {
						t.Fatalf("wheel %v changed hidden conversation: scroll %d -> %d, viewport %+v -> %+v", button, before, m.conversation.scroll, viewport, m.conversation.viewport)
					}
				}
			})
		}
	}
}

func TestConversationInputOnlyDispatchesWheel(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("\x1b[<0;10;10M\x1b[<32;11;11M\x1b[<0;11;11m\x1b[<64;10;10M\x1b[<65;10;10M"))
		writer.Close()
		done <- err
	}()
	var buttons []tea.MouseButton
	err = readConversationInput(t.Context(), reader, func(msg tea.Msg) bool {
		switch event := msg.(type) {
		case tea.MouseWheelMsg:
			buttons = append(buttons, event.Button)
		case tea.MouseMsg:
			t.Errorf("non-wheel mouse action: %T", msg)
		case tea.KeyMsg:
			t.Errorf("mouse bytes leaked as key: %v", msg)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(buttons, []tea.MouseButton{tea.MouseWheelUp, tea.MouseWheelDown}) {
		t.Fatalf("decoded wheels = %v", buttons)
	}
}

func TestConversationAnchorSurvivesResyncAndNarrowMarker(t *testing.T) {
	m := scrollShell()
	original := m.content.Blocks[0]
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	before := conversationRows(m)
	marker := design.Text(design.English, "conversation.new_messages")
	// A replay and insertion above the viewport neither move it nor claim unread output below.
	for cursor, items := range [][]state.Block{{original}, {{ID: "older", Text: "older", Done: true, Revision: 1}, original}} {
		m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: uint64(cursor + 1), Items: items}})
		if after := conversationRows(m); !reflect.DeepEqual(after, before) {
			t.Fatalf("resync moved anchored rows: %v", after)
		}
		if strings.Contains(m.View().Content, marker) {
			t.Fatal("resync claimed new output below")
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m.View()
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "conversation:fixture", Cursor: 3, Block: state.Block{ID: "new", Text: "new below", Revision: 1, Done: true}}})
	if !strings.Contains(m.View().Content, marker) {
		t.Fatal("narrow viewport dropped new-output marker")
	}
}
