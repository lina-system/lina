// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"crypto/rand"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type conversationView struct {
	workFocused  bool
	workSelected string
	workIndex    int
	workOffset   int
	workCompact  bool
	workFilter   protocol.RunningState
	undo         []state.Draft
	history      *inputHistory
	scroll       int
	viewport     conversationViewport
	id           string
	agentMissing bool
	agentPending bool
	composer     textarea.Model
	pager        textPager
	cardsFocused bool
	selected     int
	move         moveList
}

func (m *shellModel) conversationKey(msg tea.KeyMsg) tea.Cmd {
	key := term.FromTea(msg, m.events)
	// Enhanced keyboards can report Shift+/ with the produced text "?".
	if msg.Key().Text == "?" && msg.Key().Mod & ^tea.ModShift == 0 {
		key.Name = "?"
	}
	class := term.Edit
	if key.Name == "?" && m.content.Draft.Text == "" && len(m.content.Draft.Attachments) == 0 {
		class = term.Toggle
	}
	switch key.Name {
	case "enter", "esc", "ctrl+c", "ctrl+d", "tab", "shift+tab", "ctrl+o", "ctrl+t", "ctrl+w", "ctrl+r", "ctrl+p", "ctrl+b", "f6":
		class = term.Toggle
	}
	if !m.gate.Admit(key, class, m.now()) {
		return nil
	}
	if command, handled := m.moveKey(key.Name); handled {
		return command
	}
	if m.conversation.pager.open {
		return m.pagerKey(msg, key.Name)
	}
	if m.conversation.workFocused {
		return m.workPanelKey(key.Name)
	}
	if m.conversation.cardsFocused && (key.Name == "up" || key.Name == "down") {
		cards := m.resultCards()
		if len(cards) > 0 {
			if m.conversation.selected < 0 {
				m.conversation.selected = m.selectedCardIndex(cards)
			}
			if key.Name == "up" {
				m.conversation.selected = max(0, m.conversation.selected-1)
			} else {
				m.conversation.selected = min(len(cards)-1, m.conversation.selected+1)
			}
		}
		return nil
	}
	switch key.Name {
	case "up", "down":
		if m.recallInput(key.Name == "up") {
			return nil
		}
	case "?":
		if m.content.Draft.Text == "" && len(m.content.Draft.Attachments) == 0 {
			m.openPager(m.shortcutSheet())
			return nil
		}
		return m.editComposer(msg)
	case "ctrl+z":
		if n := len(m.conversation.undo); n > 0 {
			draft := m.conversation.undo[n-1]
			m.conversation.undo = m.conversation.undo[:n-1]
			return m.reduceInput(state.EditDraft{Draft: draft})
		}
		return nil
	case "ctrl+b":
		return m.openMoveList()
	case "ctrl+c":
		return m.controlC()
	case "ctrl+d":
		m.status = ConnectionStatus{State: Quit}
		return tea.Quit
	case "esc":
		if m.mutationsAllowed() && m.content.TurnActive {
			return m.reduceInput(state.Interrupt{})
		}
		return nil
	case "pgup":
		m.scrollConversation(max(1, m.size.Height/2))
		return nil
	case "pgdown":
		m.scrollConversation(-max(1, m.size.Height/2))
		return nil
	case "end":
		m.scrollConversation(-m.conversation.scroll)
		return nil
	case "ctrl+t":
		m.openPager("")
		m.conversation.pager.transcript = true
		return nil
	case "ctrl+o":
		if summary := m.toolSummary(); summary != "" {
			m.openPager(summary)
			m.conversation.pager.tool = true
			return nil
		}
		return m.openResultCard()
	case "ctrl+w":
		m.openPager(m.topicText("waiting"))
		m.conversation.pager.waiting = true
		return m.waitingRequest()
	case "ctrl+r":
		m.openRunning()
		return nil
	case "ctrl+p":
		if !m.hasCapability("progress") {
			m.openPager(design.Text(m.styles.options.Language, "capability_not_negotiated"))
			return nil
		}
		m.conversation.workFocused = true
		return m.subscribeSnapshot("progress")
	case "tab", "shift+tab":
		m.conversation.cardsFocused = !m.conversation.cardsFocused
		return nil
	case "enter":
		if m.conversation.cardsFocused {
			return m.openResultCard()
		}
		text := strings.TrimSpace(m.content.Draft.Text)
		if text == "/help" {
			m.reduceInput(state.EditDraft{})
			m.openPager(m.shortcutSheet())
			return nil
		}
		if !m.mutationsAllowed() {
			return nil
		}
		if handled, command := m.boardCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			return command
		}
		if handled, command := m.recordsCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.developerCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.talkCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.webCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.channelCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.ownerCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			return command
		}
		if handled, command := m.managementCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.accountCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if strings.HasPrefix(text, "/") && text != "/stop" && !strings.HasPrefix(text, "/queue ") {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			m.openPager(design.Text(m.styles.options.Language, "command.unknown"))
			return nil
		}
		if m.conversation.agentMissing || m.conversation.agentPending {
			return nil
		}
		if !m.hasCapability("conversation") {
			m.accounts = accountView{open: true, code: "needs_sign_in"}
			return nil
		}
		if m.conversation.id == "" {
			return nil
		}
		if text == "/stop" {
			m.reduceInput(state.EditDraft{})
			if m.content.TurnActive {
				return m.reduceInput(state.Interrupt{})
			}
			return nil
		}
		queue := strings.HasPrefix(text, "/queue ")
		if queue {
			draft := m.content.Draft
			draft.Text = strings.TrimSpace(strings.TrimPrefix(text, "/queue "))
			m.reduceInput(state.EditDraft{Draft: draft})
		}
		m.conversation.undo = nil
		return m.reduceInput(state.Send{Key: rand.Text(), Queue: queue})
	case "shift+enter", "alt+enter", "ctrl+j":
		return m.editComposer(tea.PasteMsg{Content: "\n"})
	}
	return m.editComposer(msg)
}
