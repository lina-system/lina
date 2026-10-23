// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func navigationCards(n int) []state.WorkCard {
	cards := make([]state.WorkCard, n)
	for i := range cards {
		cards[i] = state.WorkCard{ID: fmt.Sprint(i), Name: fmt.Sprintf("Work-%02d", i), Now: fmt.Sprintf("Activity-%02d", i)}
		if i < 3 {
			cards[i].Waiting = 1
		}
	}
	return cards
}

func navigationShell(width int, cards []state.WorkCard) *shellModel {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "progress", Version: "1.0"})
	m.content.Blocks = []state.Block{{Topic: "progress", Work: &state.WorkSummary{Cards: cards}}}
	m.Update(tea.PasteMsg{Content: "keep 초안"})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	return m
}

func assertSelectedWorkVisible(t *testing.T, m *shellModel, card state.WorkCard) {
	t.Helper()
	view := ansi.Strip(m.View().Content)
	if m.conversation.workSelected != card.ID {
		t.Fatalf("selected %q, want %q", m.conversation.workSelected, card.ID)
	}
	if !strings.Contains(view, card.Name) || !strings.Contains(view, card.Now) {
		t.Fatalf("selected card %q is outside the rendered panel:\n%s", card.ID, view)
	}
	if m.content.Draft.Text != "keep 초안" || !m.conversation.workFocused {
		t.Fatal("navigation changed draft or focus")
	}
}

func TestWorkPanelNavigation(t *testing.T) {
	for _, width := range []int{120, 100} {
		for _, n := range []int{20, 50} {
			t.Run(fmt.Sprintf("%dx34/N=%d", width, n), func(t *testing.T) {
				cards := navigationCards(n)
				m := navigationShell(width, cards)
				m.View()
				for i := 1; i < n; i++ {
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					assertSelectedWorkVisible(t, m, cards[i])
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				assertSelectedWorkVisible(t, m, cards[n-1])
				for i := n - 2; i >= 0; i-- {
					m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
					assertSelectedWorkVisible(t, m, cards[i])
				}
				m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
				assertSelectedWorkVisible(t, m, cards[0])
			})
		}
	}
}

func TestWorkPanelNavigationFoldedAndHiddenCounts(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ascii=%t", language, ascii), func(t *testing.T) {
				cards := navigationCards(20)
				cards[1].State, cards[1].Waiting = protocol.RunningStateDone, 0
				// Done work with an outstanding person request remains a card.
				cards[2].State = protocol.RunningStateDone
				m := navigationShell(120, cards)
				m.styles = NewStyles(Options{Profile: TrueColor, Language: language, ASCII: ascii})
				m.View()
				m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				assertSelectedWorkVisible(t, m, cards[2])
				for i := 3; i < len(cards); i++ {
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				}
				assertSelectedWorkVisible(t, m, cards[19])
				rows := m.workPanelRows(40, 10)
				text := strings.Join(rows, "\n")
				tail := "…"
				if ascii {
					tail = "..."
				}
				// Three visible cards, one folded, sixteen hidden (two need input).
				for _, want := range []string{cards[19].Now, fmt.Sprintf(design.Text(language, "work.done"), 1), fmt.Sprintf(design.Text(language, "work.more"), tail, 16), fmt.Sprintf(design.Text(language, "work.needs"), 2)} {
					if !strings.Contains(text, want) {
						t.Fatalf("missing %q in %q", want, rows)
					}
				}
				if len(rows) > 10 || strings.Contains(text, cards[1].Name) {
					t.Fatalf("folded card or row overflow: %q", rows)
				}
			})
		}
	}
}

func TestWorkPanelNavigationReconcileAndResize(t *testing.T) {
	cards := navigationCards(50)
	m := navigationShell(120, cards)
	m.View()
	for i := 1; i < 45; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	assertSelectedWorkVisible(t, m, cards[44])
	for _, size := range [][2]int{{100, 12}, {120, 34}, {40, 10}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		assertSelectedWorkVisible(t, m, cards[44])
	}
	// Simulate replacement of Core's ordered projection while keeping its IDs.
	cards[44], cards[2] = cards[2], cards[44]
	assertSelectedWorkVisible(t, m, cards[2])
	m.content.Blocks[0].Work.Cards = append(cards[:2:2], cards[3:]...)
	assertSelectedWorkVisible(t, m, cards[3])
	m.content.Blocks[0].Work.Cards = nil
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.View()
	if m.conversation.workSelected != "" {
		t.Fatal("empty panel retained a stale selection")
	}
	m.content.Blocks[0].Work.Cards = cards[:1]
	assertSelectedWorkVisible(t, m, cards[0])
}
