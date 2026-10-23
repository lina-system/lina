// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
)

func newConversationView(styles Styles) conversationView {
	c := textarea.New()
	c.Prompt = ""
	c.Placeholder = fmt.Sprintf(design.Text(styles.options.Language, "composer.placeholder"), "LINA")
	c.ShowLineNumbers = false
	c.CharLimit = 16384
	c.MaxWidth, c.MaxHeight = 0, 6
	c.DynamicHeight = true
	c.MinHeight = 1
	c.MaxContentHeight = 16384
	c.SetStyles(textarea.Styles{Focused: textarea.StyleState{Text: styles.Text, Placeholder: styles.Muted}, Blurred: textarea.StyleState{Text: styles.Muted}})
	c.SetVirtualCursor(false)
	c.SetWidth(max(1, styles.options.Width-styles.composerPromptWidth()))
	c.SetHeight(1)
	c.Focus()
	// Clipboard reads are never an application action; terminal PasteMsg owns paste.
	c.KeyMap.Paste.SetEnabled(false)
	c.KeyMap.CopySelection.SetEnabled(false)
	return conversationView{composer: c, selected: -1}
}

func (m *shellModel) syncComposer() {
	m.syncInputHistory()
	c := &m.conversation.composer
	placeholder := "composer.placeholder"
	if m.disambiguated {
		placeholder = "composer.shift_enter"
	}
	c.Placeholder = fmt.Sprintf(design.Text(m.styles.options.Language, placeholder), m.conversationName())
	if c.Value() != m.content.Draft.Text {
		c.SetValue(m.content.Draft.Text)
	}
	c.SetWidth(max(1, m.size.Width-m.styles.composerPromptWidth()))
}

func (s Styles) composerPrompt() string { return s.listBullet() + " " }

func (s Styles) composerPromptWidth() int { return (render.Unicode{}).Width(s.composerPrompt()) }

func (m *shellModel) editComposer(msg tea.Msg) tea.Cmd {
	if m.conversation.pager.open {
		return nil
	}
	m.syncComposer()
	c, command := m.conversation.composer.Update(msg)
	m.conversation.composer = c
	draft := m.content.Draft
	if draft.Text != c.Value() {
		m.conversation.history = nil
		m.conversation.undo = append(m.conversation.undo, draft)
		if len(m.conversation.undo) > 64 {
			m.conversation.undo = m.conversation.undo[1:]
		}
	}
	draft.Text = c.Value()
	m.content, _ = state.Reduce(m.content, state.EditDraft{Draft: draft})
	return command
}

func (m *shellModel) reduceInput(msg state.Msg) tea.Cmd {
	switch msg.(type) {
	case state.FocusConversation:
		m.restoreHistoryDraft()
	case state.EditDraft, state.Send:
		m.conversation.history = nil
	}
	var commands []state.Cmd
	m.content, commands = state.Reduce(m.content, msg)
	m.syncComposer()
	m.refreshBlocks()
	return tea.Batch(m.effectCmds(commands)...)
}
