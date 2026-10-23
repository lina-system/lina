// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func longApprovalModel(t *testing.T) *shellModel {
	t.Helper()
	m := cardModel(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	var lines []string
	for i := range 100 {
		lines = append(lines, fmt.Sprintf("target-%03d %s", i, strings.Repeat("한글", 35)))
	}
	m.content.Cards[0].Targets[0].Value = strings.Join(lines, "\n")
	return m
}

func readApprovalPages(t *testing.T, m *shellModel) {
	t.Helper()
	c, _ := m.content.CurrentCard()
	seen := ""
	for range 100 {
		seen += m.cardContent(c) + "\n"
		before := m.cardScroll
		if cmd, _ := m.cardKey(term.Key{Name: "pgdown", Events: true}); cmd != nil {
			t.Fatal("reading sent a decision")
		}
		if before == m.cardScroll {
			break
		}
	}
	for i := range 100 {
		if !strings.Contains(seen, fmt.Sprintf("target-%03d", i)) {
			t.Errorf("page navigation skipped target-%03d", i)
		}
	}
	if !strings.Contains(m.cardContent(c), design.Text(m.styles.options.Language, "card.confirm")) {
		t.Fatal("all target pages read at 120x34 but separate confirmation remains unavailable")
	}
}

func TestApprovalReadingPagesThenSeparateConfirm(t *testing.T) {
	m := longApprovalModel(t)
	c, _ := m.content.CurrentCard()
	m.cardContent(c)
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("unread targets approved")
	}
	readApprovalPages(t, m)
	if m.content.CardView.Pending {
		t.Fatal("reading or selecting approved without a separate confirmation")
	}
	cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true})
	if cmd == nil || cmd().(shellCardAnswer).Request.Choice != protocol.ApprovalChoiceOnce {
		t.Fatal("read targets did not allow one-time approval")
	}
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("duplicate confirmation sent")
	}
}

func TestApprovalReadingRequiresEveryRenderedPage(t *testing.T) {
	m := longApprovalModel(t)
	c, _ := m.content.CurrentCard()
	m.cardContent(c)
	// Navigation without painting the intermediate pages cannot count as reading.
	for range 100 {
		m.cardKey(term.Key{Name: "pgdown", Events: true})
	}
	m.cardContent(c)
	m.cardKey(term.Key{Name: "1", Events: true})
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("skipped pages counted as read")
	}
	m.cardScroll = 0
	readApprovalPages(t, m)
}

func TestApprovalReadingInvalidation(t *testing.T) {
	for _, change := range []string{"width", "height", "resize-back", "revision", "token", "epoch", "reopen", "reconnect"} {
		t.Run(change, func(t *testing.T) {
			m := longApprovalModel(t)
			readApprovalPages(t, m)
			m.cardKey(term.Key{Name: "1", Events: true})
			c, _ := m.content.CurrentCard()
			switch change {
			case "width":
				m.Update(tea.WindowSizeMsg{Width: 119, Height: 34})
			case "height":
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 33})
			case "resize-back":
				m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
			case "revision", "token":
				c.Revision++
				if change == "token" {
					c.Token = "new-group"
				}
				m.applyData(state.PutCard{Card: c})
			case "epoch":
				m.content.Epoch = "2"
			case "reopen":
				m.cardKey(term.Key{Name: "esc", Events: true})
				m.cardKey(term.Key{Name: "f6", Events: true})
			case "reconnect":
				m.beginGeneration(shellBegin{Generation: m.generation + 1})
				m.status.State = Live
			}
			m.cardKey(term.Key{Name: "1", Events: true})
			if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
				t.Fatal("old reading record allowed confirmation")
			}
		})
	}
}

func TestApprovalReadingStillNeedsSelectionAndPress(t *testing.T) {
	m := longApprovalModel(t)
	readApprovalPages(t, m)
	if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd != nil {
		t.Fatal("reading substituted for selection")
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	for _, event := range []term.KeyEvent{term.Repeat, term.Release} {
		if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true, Event: event}); cmd != nil {
			t.Fatal("non-press confirmed")
		}
	}
}

func TestApprovalReadingTerminalSizes(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 10}, {Width: 80, Height: 24}, {Width: 120, Height: 34}} {
		for _, language := range []design.Language{design.English, design.Korean} {
			t.Run(fmt.Sprintf("%dx%d/%s", size.Width, size.Height, language), func(t *testing.T) {
				m := longApprovalModel(t)
				options := m.styles.options
				options.Language = language
				m.styles = NewStyles(options)
				m.Update(size)
				readApprovalPages(t, m)
				// Duplicate size notifications and replayed cards preserve reading.
				m.Update(size)
				c, _ := m.content.CurrentCard()
				m.applyData(state.PutCard{Card: c})
				m.cardKey(term.Key{Name: "1", Events: true})
				if cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true}); cmd == nil {
					t.Fatal("read targets did not remain confirmable")
				}
			})
		}
	}
}
