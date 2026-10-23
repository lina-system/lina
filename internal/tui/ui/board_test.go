// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestBoardGroupsCoreRowsAndRetainsSelection(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"})
	m.content.Blocks = []state.Block{
		{ID: "hidden-done", Topic: "running", RunID: "run-done", RunState: protocol.RunningStateDone, Text: "DONE-SUMMARY"},
		{ID: "hidden-running", Topic: "running", RunID: "run-active", RunState: protocol.RunningStateInProgress, Text: "ACTIVE-SUMMARY"},
		{ID: "hidden-review", Topic: "running", RunID: "run-review", RunState: protocol.RunningStateReadyForReview, Text: "REVIEW-SUMMARY"},
		{ID: "hidden-input", Topic: "running", RunID: "run-input", RunState: protocol.RunningStateNeedsInput, Text: "INPUT-SUMMARY"},
		{ID: "hidden-quota", Topic: "running", RunID: "run-quota", RunState: protocol.RunningStateWaiting, RunReason: protocol.RunningReasonQuota, Text: "QUOTA-SUMMARY"},
	}
	m.Update(tea.PasteMsg{Content: "/board"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.conversation.pager.open || len(m.content.Blocks) != 5 {
		t.Fatal("/board did not open a local board")
	}
	_ = cmd
	text := ansi.Strip(m.View().Content)
	last := -1
	for _, marker := range []string{design.Text(design.English, "waiting"), "INPUT-SUMMARY", "ACTIVE-SUMMARY", "QUOTA-SUMMARY", "DONE-SUMMARY", "REVIEW-SUMMARY"} {
		pos := strings.Index(text, marker)
		if pos <= last {
			t.Fatalf("group/order missing %q: %s", marker, text)
		}
		last = pos
	}
	if strings.Contains(text, "hidden-") || strings.Contains(text, "run-active") {
		t.Fatal("board exposed identities")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // Unfold locally.
	// A terminal row changes group. Selection remains on the same identity.
	m.content.Blocks[0].RunState = protocol.RunningStateInProgress
	m.Update(shellData{Generation: m.generation, Message: state.Notice{ID: "test-refresh", Code: "live"}})
	if !strings.Contains(m.conversation.pager.text, "> - ") {
		t.Fatal("board lost unfolded selection on refresh")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	if command == nil {
		t.Fatal("selected run cannot be stopped")
	}
	stop, ok := command().(shellManagement).Request.(protocol.Stop)
	if !ok || stop.RunID == nil || *stop.RunID != "run-input" {
		t.Fatalf("action changed target: %+v", stop)
	}
}

func TestBoardUnavailableStaysLocal(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/board"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !m.conversation.pager.open || len(m.content.Blocks) != 0 {
		t.Fatal("unavailable board became model input")
	}
}

func TestBoardClosedOrReplacedReadCannotReopen(t *testing.T) {
	for _, refused := range []bool{false, true} {
		m := conversationShell()
		old := &boardView{requestID: "old"}
		m.openPager("another view")
		rows := []protocol.WaitingRow{{Ref: protocol.WaitingRef{Source: "outside", Key: "old"}}}
		s := managementSession{model: m, pending: map[string]protocol.Payload{"old": protocol.WaitingGet{}}, boardPending: map[string]*boardView{"old": old}}
		frame := protocol.Envelope{ReplyTo: stringPointer("old"), Payload: protocol.Waiting{Rows: &rows}}
		if refused {
			frame.Error = &protocol.Error{Code: protocol.ErrorUnavailable}
		}
		if ok, err := s.response(t.Context(), nil, frame); err != nil || !ok {
			t.Fatalf("stale response: %v", err)
		}
		if m.conversation.pager.text != "another view" || m.conversation.pager.board != nil {
			t.Fatal("stale board response changed the current view")
		}
	}
}
