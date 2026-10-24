// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// moveList is the open list of other candidates, "new" and "none" for one
// person message. A move is one choice from this list.
type moveList struct {
	open     bool
	input    string
	choices  []protocol.BindingCandidate
	index    int
	busy     bool
	failed   bool
	received bool
}

// chipBlock returns the newest person message that carries a chip.
func (m *shellModel) chipBlock() *state.Block {
	for i := len(m.content.Blocks) - 1; i >= 0; i-- {
		b := m.content.Blocks[i]
		if b.Binding == nil || !b.Binding.Show || b.Binding.InputID == "" {
			continue
		}
		if b.Speaker != protocol.ConversationSpeakerPerson && !b.Local {
			continue
		}
		if !m.conversationBlock(b) {
			continue
		}
		return &m.content.Blocks[i]
	}
	return nil
}

// openMoveList asks Core for the choices of the newest chipped message.
func (m *shellModel) openMoveList() tea.Cmd {
	block := m.chipBlock()
	if block == nil {
		return nil
	}
	m.conversation.move = moveList{open: true, input: block.Binding.InputID, busy: true}
	return m.bindingGet(m.conversation.move.input)
}

func (m *shellModel) bindingGet(input string) tea.Cmd {
	request := state.BindingGetRequest{Gen: m.generation, InputID: input}
	return func() tea.Msg { return shellEffect{Generation: m.generation, Message: request} }
}

func (m *shellModel) bindingMove(input string, target protocol.BindingTarget) tea.Cmd {
	m.conversation.move.busy = true
	request := state.BindingMoveRequest{Gen: m.generation, InputID: input, Target: target}
	return func() tea.Msg { return shellEffect{Generation: m.generation, Message: request} }
}

// moveKey handles the open list. It reports whether the key belonged to it.
func (m *shellModel) moveKey(key string) (tea.Cmd, bool) {
	move := &m.conversation.move
	if !move.open {
		return nil, false
	}
	switch key {
	case "esc", "ctrl+b", "q":
		*move = moveList{}
		return nil, true
	case "up":
		if move.index > 0 {
			move.index--
		}
		return nil, true
	case "down":
		if move.index+1 < len(move.choices) {
			move.index++
		}
		return nil, true
	case "enter":
		return m.chooseMove(move.index), true
	case "n":
		for i, choice := range move.choices {
			if choice.State == "new" {
				return m.chooseMove(i), true
			}
		}
		return nil, true
	case "0":
		for i, choice := range move.choices {
			if choice.State == "none" {
				return m.chooseMove(i), true
			}
		}
		return nil, true
	}
	if number, err := strconv.Atoi(key); err == nil && number >= 1 && number <= len(move.choices) {
		return m.chooseMove(number - 1), true
	}
	return nil, true
}

func (m *shellModel) chooseMove(index int) tea.Cmd {
	move := &m.conversation.move
	if move.busy || index < 0 || index >= len(move.choices) {
		return nil
	}
	choice := move.choices[index]
	move.index = index
	return m.bindingMove(move.input, moveTarget(choice))
}

// moveTarget turns one listed choice back into the wire target.
func moveTarget(choice protocol.BindingCandidate) protocol.BindingTarget {
	target := protocol.BindingTarget{Class: choice.State}
	switch choice.State {
	case "existing":
		id, kind := choice.ID, choice.Kind
		target.ID, target.Kind = &id, &kind
	case "new":
		kind := "todo"
		target.Kind = &kind
	}
	return target
}

// bindingResponse consumes the chip, the move list and the move result.
func (m *shellModel) bindingResponse(e protocol.Envelope) bool {
	switch p := e.Payload.(type) {
	case protocol.Binding:
		if !m.conversation.move.open || m.conversation.move.input != p.InputID {
			return true
		}
		m.conversation.move.choices = p.Candidates
		m.conversation.move.busy = false
		m.conversation.move.received = true
		m.conversation.move.index = 0
		if len(p.Candidates) == 0 {
			m.conversation.move.failed = true
		}
		return true
	case protocol.BindingMoveResult:
		m.conversation.move.busy = false
		m.conversation.move.open = false
		m.conversation.move = moveList{}
		return true
	}
	return false
}

// moveRows renders the open list inside the conversation area.
func (m *shellModel) moveRows(width int) []string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	move := &m.conversation.move
	lines := []string{m.styles.Title.Render(render.Truncate(text("binding.title"), width, render.Unicode{}))}
	if move.failed {
		return append(lines, m.styles.Muted.Render(render.Truncate(text("binding.failed"), width, render.Unicode{})))
	}
	if move.busy || !move.received {
		return append(lines, m.styles.Muted.Render(render.Truncate(text("binding.pending"), width, render.Unicode{})))
	}
	for i, choice := range move.choices {
		label := choice.Title
		if choice.State == "new" {
			label = text("binding.new")
		} else if choice.State == "none" {
			label = text("binding.none")
		}
		line := "  " + label
		if i == move.index {
			line = m.styles.Accent.Render("› ") + label
		}
		lines = append(lines, render.Truncate(line, width, render.Unicode{}))
	}
	return lines
}
