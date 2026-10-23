// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func selectionPanel(t *testing.T, width int, ascii bool) (*shellModel, []state.WorkCard) {
	t.Helper()
	m := conversationShell()
	m.styles = NewStyles(Options{Profile: Monochrome, ASCII: ascii})
	m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "progress", Version: "1.0"})
	at := time.Date(2026, 10, 23, 12, 42, 0, 0, time.UTC)
	m.now = func() time.Time { return at }
	cards := []state.WorkCard{}
	for i := 0; i < 50; i++ {
		c := state.WorkCard{ID: fmt.Sprintf("task-%02d", i), Name: fmt.Sprintf("Task %02d", i), Now: fmt.Sprintf("Activity %02d", i), Started: at.Add(-42 * time.Minute), State: protocol.RunningStateInProgress}
		if i < 3 {
			c.Waiting = 1
			c.State = protocol.RunningStateNeedsInput
		}
		cards = append(cards, c)
		if i == 1 || i == 18 {
			cards = append(cards, state.WorkCard{ID: fmt.Sprint("done-", i), Name: "Folded completion", State: protocol.RunningStateDone})
		}
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "0", Topic: "progress", Cursor: 1, Items: []state.Block{{ID: "overview", Work: &state.WorkSummary{Cards: cards}}}}})
	history := []state.Block{}
	for i := 0; i < 40; i++ {
		history = append(history, state.Block{ID: fmt.Sprint("history-", i), Text: fmt.Sprintf("History %02d", i), Done: true, Revision: 1, Speaker: protocol.ConversationSpeakerAgent})
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "0", Topic: "conversation:" + m.conversation.id, Cursor: 1, Items: history}})
	m.Update(tea.PasteMsg{Content: "Keep this draft"})
	m.View()
	m.scrollConversation(10)
	m.View()
	return m, cards
}

func selectedPanelCard(t *testing.T, m *shellModel, want int) {
	t.Helper()
	frame := ansi.Strip(m.View().Content)
	plan := m.conversationPlan()
	width, height := plan.SidePane.Width, plan.SidePane.Height
	if width == 0 {
		width, height = plan.Conversation.Width, plan.Conversation.Height
	}
	rows := m.workPanelRows(width, height)
	marker := m.styles.tokens.GlyphUnicodeAction
	if m.styles.options.ASCII {
		marker = m.styles.tokens.GlyphAsciiAction
	}
	selected := ""
	shown, hiddenWaiting := 0, 3
	for i, row := range rows {
		if (render.Unicode{}).Width(row) > width {
			t.Fatalf("card exceeds panel: %q", row)
		}
		if strings.HasPrefix(row, marker+" ") {
			if selected != "" {
				t.Fatal("multiple selected cards")
			}
			selected = row
			if i+1 >= len(rows) || strings.TrimSpace(rows[i+1]) != fmt.Sprintf("Activity %02d", want) {
				t.Fatalf("selected card lost its activity: %q", rows)
			}
		}
		if strings.HasPrefix(row, "  Activity ") {
			index, err := strconv.Atoi(strings.TrimPrefix(row, "  Activity "))
			if err != nil {
				t.Fatal(err)
			}
			shown++
			if index < 3 {
				hiddenWaiting--
			}
		}
	}
	if !strings.HasPrefix(selected, fmt.Sprintf("%s Task %02d ", marker, want)) || !strings.HasSuffix(selected, "42m") || !strings.Contains(frame, selected) {
		t.Fatalf("selected Task %02d is not visibly rendered: %q\n%s", want, selected, frame)
	}
	if m.conversation.workSelected != fmt.Sprintf("task-%02d", want) {
		t.Fatalf("selected a folded or wrong card: %s", m.conversation.workSelected)
	}
	ellipsis := "…"
	if m.styles.options.ASCII {
		ellipsis = "..."
	}
	overflow := fmt.Sprintf(design.Text(m.styles.options.Language, "work.more"), ellipsis, 50-shown)
	if hiddenWaiting > 0 {
		separator := m.styles.tokens.GlyphUnicodeActivitySeparator
		if m.styles.options.ASCII {
			separator = m.styles.tokens.GlyphAsciiActivitySeparator
		}
		overflow += " " + separator + " " + fmt.Sprintf(design.Text(m.styles.options.Language, "work.needs"), hiddenWaiting)
	}
	if rows[len(rows)-1] != overflow {
		t.Fatalf("hidden cards before/after window counted incorrectly: %q want %q", rows[len(rows)-1], overflow)
	}
	if rows[len(rows)-2] != fmt.Sprintf(design.Text(m.styles.options.Language, "work.done"), 2) {
		t.Fatal("folded completion count changed")
	}
}

func TestWorkPanelSelectionNavigatesOverflow(t *testing.T) {
	for _, width := range []int{120, 100} {
		for _, ascii := range []bool{false, true} {
			t.Run(fmt.Sprintf("width=%d/ascii=%t", width, ascii), func(t *testing.T) {
				m, _ := selectionPanel(t, width, ascii)
				anchor := m.conversation.viewport.anchor
				m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
				selectedPanelCard(t, m, 0)
				for i := 1; i < 50; i++ {
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					selectedPanelCard(t, m, i)
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				selectedPanelCard(t, m, 49)
				if m.conversation.viewport.anchor != anchor {
					t.Fatal("panel navigation scrolled conversation")
				}
				m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
				selectedPanelCard(t, m, 49)
				m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
				for i := 48; i >= 0; i-- {
					m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
					selectedPanelCard(t, m, i)
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
				selectedPanelCard(t, m, 0)
				if m.content.Draft.Text != "Keep this draft" || !m.conversation.workFocused {
					t.Fatal("navigation changed draft/focus")
				}
				// Resize can reflow the conversation; navigation alone must retain its anchor.
				m.View()
				anchor = m.conversation.viewport.anchor
				m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				selectedPanelCard(t, m, 1)
				if m.conversation.viewport.anchor != anchor {
					t.Fatal("panel navigation scrolled conversation")
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				if m.conversation.workFocused || m.content.Draft.Text != "Keep this draft" {
					t.Fatal("Esc lost composer draft")
				}
				marker := m.styles.tokens.GlyphUnicodeAction
				if m.styles.options.ASCII {
					marker = m.styles.tokens.GlyphAsciiAction
				}
				if strings.Contains(strings.Join(m.workPanelRows(40, 29), "\n"), marker+" Task") || !strings.Contains(m.workPanelRows(40, 29)[1], "Task 00") {
					t.Fatal("unfocused panel lost attention-first overview")
				}
			})
		}
	}
}

func TestWorkPanelSelectionSurvivesUpdates(t *testing.T) {
	m, cards := selectionPanel(t, 120, false)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	for i := 0; i < 30; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m.View()
	anchor := m.conversation.viewport.anchor
	apply := func(seq uint64, next []state.WorkCard) {
		t.Helper()
		m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "0", Topic: "progress", Cursor: seq, Items: []state.Block{{ID: "overview", Work: &state.WorkSummary{Cards: next}}}}})
		m.View()
		if m.content.Draft.Text != "Keep this draft" || !m.conversation.workFocused || m.conversation.viewport.anchor != anchor {
			t.Fatal("work update changed conversation")
		}
	}
	at := slices.IndexFunc(cards, func(c state.WorkCard) bool { return c.ID == "task-30" })
	selected := cards[at]
	reordered := append([]state.WorkCard{selected}, append(slices.Clone(cards[:at]), cards[at+1:]...)...)
	apply(2, reordered)
	selectedPanelCard(t, m, 30)
	apply(3, reordered[1:])
	if m.conversation.workSelected != "task-00" || !strings.Contains(m.View().Content, "Task 00") {
		t.Fatal("removed selection did not move to its replacement")
	}
	apply(4, []state.WorkCard{})
	if m.conversation.workSelected != "" {
		t.Fatal("empty panel retained stale selection")
	}
	apply(5, cards)
	selectedPanelCard(t, m, 0)
	completed := slices.Clone(cards)
	completed[0].State = protocol.RunningStateDone
	completed[0].Waiting = 0
	apply(6, completed)
	if m.conversation.workSelected != "task-01" {
		t.Fatal("selection remained on a folded completion")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.View()
	if m.conversation.workSelected != "task-02" {
		t.Fatal("navigation stopped on a folded completion")
	}
}
