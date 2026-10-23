// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"slices"

	"github.com/thisisjun786/lina/internal/tui/state"
)

type inputHistory struct {
	id     string
	draft  state.Draft
	column int // Recall starts on the first visual line of the saved draft.
}

func (m *shellModel) restoreHistoryDraft() {
	h := m.conversation.history
	if h == nil {
		return
	}
	m.conversation.history = nil
	m.content, _ = state.Reduce(m.content, state.EditDraft{Draft: h.draft})
	c := &m.conversation.composer
	c.SetValue(h.draft.Text)
	for c.Line() > 0 || c.LineInfo().RowOffset > 0 {
		c.CursorUp()
	}
	c.SetCursorColumn(h.column)
}

func (m *shellModel) syncInputHistory() {
	h := m.conversation.history
	if h == nil {
		return
	}
	for _, entry := range m.content.InputHistory(m.conversation.id) {
		if entry.ID == h.id {
			if m.content.Draft.Text != entry.Text {
				m.content, _ = state.Reduce(m.content, state.EditDraft{Draft: state.Draft{Text: entry.Text}})
			}
			return
		}
	}
	// A replaced snapshot, revocation or focus change removed the selection.
	m.restoreHistoryDraft()
}

func (m *shellModel) recallInput(older bool) bool {
	m.syncComposer()
	c := &m.conversation.composer
	line := c.LineInfo()
	if older && (c.Line() != 0 || line.RowOffset != 0) ||
		!older && (c.Line() != c.LineCount()-1 || line.RowOffset != line.Height-1) {
		return false
	}
	entries := m.content.InputHistory(m.conversation.id)
	h := m.conversation.history
	index := -1
	if h != nil {
		index = slices.IndexFunc(entries, func(e state.InputHistoryEntry) bool { return e.ID == h.id })
	}
	if older {
		if len(entries) == 0 {
			return false
		}
		index = min(index+1, len(entries)-1)
		if h == nil {
			draft := m.content.Draft
			draft.Attachments = slices.Clone(draft.Attachments)
			h = &inputHistory{draft: draft, column: c.Column()}
			m.conversation.history = h
		}
	} else {
		if h == nil {
			return false
		}
		index--
		if index < 0 {
			m.restoreHistoryDraft()
			return true
		}
	}
	h.id = entries[index].ID
	m.content, _ = state.Reduce(m.content, state.EditDraft{Draft: state.Draft{Text: entries[index].Text}})
	c.SetValue(entries[index].Text)
	if older {
		for c.Line() > 0 || c.LineInfo().RowOffset > 0 {
			c.CursorUp()
		}
	}
	return true
}
