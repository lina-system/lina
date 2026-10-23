// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type boardView struct {
	state.Board
	waiting   []protocol.WaitingRow
	requestID string
}

func (m *shellModel) boardCommand(text string) (bool, tea.Cmd) {
	if text != "/board" {
		return false, nil
	}
	if !m.hasCapability("running") {
		m.openPager(design.Text(m.styles.options.Language, "capability_not_negotiated"))
		return true, nil
	}
	m.openPager("")
	m.conversation.pager.board = &boardView{}
	m.refreshBoard()
	return true, m.waitingRequest()
}

func (m *shellModel) refreshBoard() {
	p := &m.conversation.pager
	if !p.open || p.board == nil {
		return
	}
	b := p.board
	b.Board = b.Replace(m.topicBlocks("running"), b.waiting)
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	width, height := m.size.Width, m.size.Height-1
	if pane := m.conversationPlan().SidePane; pane.Width > 0 {
		width, height = pane.Width, pane.Height
	}
	lines := []string{text("board.heading"), text("board.keys"), ""}
	selectedLine := 0
	for group, heading := range []string{"waiting", "running", "board.done"} {
		lines = append(lines, text(heading))
		count := 0
		for _, row := range b.Rows {
			if row.Group != group {
				continue
			}
			count++
			marker := "  "
			if row.ID == b.Selected {
				marker = "> "
				selectedLine = len(lines)
			}
			fold := "+ "
			if b.Expanded[row.ID] {
				fold = "- "
			}
			lines = append(lines, render.Truncate(marker+fold+oneLaneLine(row.Text), max(1, width-2), render.Unicode{}))
			if b.Expanded[row.ID] {
				lines = append(lines, "    "+oneLaneLine(row.Text))
				if row.Detail != "" {
					lines = append(lines, "    "+oneLaneLine(row.Detail))
				}
				var actions []string
				if row.RunID != "" && row.Group == 2 {
					actions = append(actions, text("board.open"), text("board.dismiss"))
				}
				if row.CanStop() {
					actions = append(actions, text("board.stop"))
				}
				if row.CanNote() {
					actions = append(actions, text("board.note"))
				}
				if row.Group == 0 && m.hasBoardDecision() {
					actions = append(actions, text("board.decisions"))
				}
				if len(actions) > 0 {
					lines = append(lines, "    "+strings.Join(actions, " · "))
				}
			}
		}
		if count == 0 {
			lines = append(lines, "  "+text("board.empty"))
		}
		lines = append(lines, "")
	}
	p.text = strings.Join(lines, "\n")
	// Expanded content can wrap. Locate the selected marker in rendered rows.
	for i, line := range m.pagerLines() {
		if strings.HasPrefix(line, "> ") {
			selectedLine = i
			break
		}
	}
	if selectedLine < p.offset {
		p.offset = selectedLine
	}
	if selectedLine >= p.offset+max(1, height) {
		p.offset = selectedLine - max(1, height) + 1
	}
}

func (m *shellModel) boardKey(name string) (bool, tea.Cmd) {
	p := &m.conversation.pager
	b := p.board
	switch name {
	case "up", "k":
		b.Board = b.Move(-1)
	case "down", "j":
		b.Board = b.Move(1)
	case "enter":
		b.Board = b.Toggle()
	case "r":
		return true, m.waitingRequest()
	case "f6":
		for _, card := range m.content.Cards {
			if card.Outcome == "" {
				p.open = false
				m.content, _ = state.Reduce(m.content, state.OpenCard{ID: card.ID})
				m.cardGate.CardChanged(m.now())
				m.cardField, m.cardScroll = 0, 0
				break
			}
		}
		return true, nil
	case "o", "s", "d", "n":
		if !m.mutationsAllowed() {
			return true, nil
		}
		for _, row := range b.Rows {
			if row.ID != b.Selected || !b.Expanded[row.ID] {
				continue
			}
			switch name {
			case "o":
				if row.RunID != "" && row.Group == 2 {
					return true, m.runningRequest(row.RunID, "open", nil)
				}
			case "d":
				if row.RunID != "" && row.Group == 2 {
					return true, m.runningRequest(row.RunID, "dismiss", nil)
				}
			case "s":
				if row.CanStop() {
					_, cmd := m.ownerCommand("/stop " + row.RunID)
					return true, cmd
				}
			case "n":
				if row.CanNote() {
					p.open = false
					m.reduceInput(state.EditDraft{Draft: state.Draft{Text: "/note " + row.RunID + " "}})
					return true, nil
				}
			}
		}
	default:
		return false, nil
	}
	m.refreshBoard()
	return true, nil
}

func (m *shellModel) hasBoardDecision() bool {
	for _, card := range m.content.Cards {
		if card.Outcome == "" {
			return true
		}
	}
	return false
}
