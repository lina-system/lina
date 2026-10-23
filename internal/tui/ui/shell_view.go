// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
)

// Index only changed blocks. View then requests a bounded window from the
// indexed stream rather than reflowing the entire history on each status tick.
func (m *shellModel) refreshBlocks() {
	next := make(map[string]shellBlock, len(m.content.Blocks))
	for _, block := range m.content.Blocks {
		if block.Card != nil {
			card := *block.Card
			for _, current := range m.content.Cards {
				if current.ID == card.ID {
					card = current
					break
				}
			}
			block.Text = m.cardSummary(card)
		}
		cached, ok := m.blocks[block.ID]
		if m.conversation.scroll > 0 && conversationBlock(block) && (!ok || cached.text != block.Text) {
			if m.conversation.viewport.changed == nil {
				m.conversation.viewport.changed = make(map[string]bool)
			}
			m.conversation.viewport.changed[block.ID] = true
		}
		if !ok || cached.revision != block.Revision || cached.text != block.Text || cached.done != block.Done {
			config := render.Config{Width: max(1, m.size.Width)}
			stream := render.NewStream(block.ID, config, nil)
			if ok && !cached.done && !block.Done && strings.HasPrefix(block.Text, cached.text) {
				stream = cached.stream
				stream.Append(block.Revision, strings.TrimPrefix(block.Text, cached.text))
			} else if block.Done {
				stream.Seal(block.Revision, block.Text)
			} else {
				stream.Append(block.Revision, block.Text)
			}
			cached = shellBlock{block.Text, block.Revision, block.Done, stream}
		}
		next[block.ID] = cached
	}
	m.blocks = next
}

func (m *shellModel) View() tea.View {
	view := tea.NewView(m.viewContent(true))
	view.AltScreen = true
	view.ReportFocus = true
	view.KeyboardEnhancements.ReportEventTypes = true
	if m.hasComposer() && m.status.State.active() {
		plan := m.conversationPlan()
		if plan.Tier != layout.TooSmall && !m.conversation.pager.open && !m.accounts.open && !m.web.open && !m.channels.open && !m.talk.open && m.content.CardView.ID == "" {
			view.Cursor = m.conversation.composer.Cursor()
			if view.Cursor != nil {
				view.Cursor.Position.X += m.styles.composerPromptWidth()
				view.Cursor.Position.Y += plan.Composer.Y
			}
		}
	}
	return view
}

// Notices stay outside history. A capability notice is no longer current when
// a later welcome grants a different set of lists.
func (m *shellModel) localNotice() string {
	if m.conversation.agentMissing {
		return design.Text(m.styles.options.Language, "agents.choose_required")
	}
	for i := len(m.content.Notices) - 1; i >= 0; i-- {
		code := m.content.Notices[i].Code
		switch code {
		case "sandbox.unavailable":
			unavailable := false
			for _, block := range m.content.Blocks {
				if block.Topic == "core" && block.ShellFileToolsAvailable != nil {
					unavailable = !*block.ShellFileToolsAvailable
				}
			}
			if !unavailable {
				continue
			}
		case "shell.lists_unavailable":
			if m.hasCapability("running") || m.hasCapability("waiting") {
				continue
			}
		case "shell.running_unavailable":
			if m.hasCapability("running") || !m.hasCapability("waiting") {
				continue
			}
		case "shell.waiting_unavailable":
			if !m.hasCapability("running") || m.hasCapability("waiting") {
				continue
			}
		}
		return design.Text(m.styles.options.Language, code)
	}
	return ""
}

func (m *shellModel) viewContent(showNotice bool) string {
	if c, ok := m.content.CurrentCard(); ok && m.size.Width >= 40 && m.size.Height >= 10 {
		return m.cardContent(c)
	}
	if m.size.Width <= 0 || m.size.Height <= 0 {
		return ""
	}
	if m.web.open {
		return m.webContent()
	}
	if m.talk.open {
		return m.talkContent()
	}
	if m.channels.open {
		return m.channelContent()
	}
	if m.accounts.open {
		return m.accountContent()
	}
	if m.hasComposer() && m.status.State.active() {
		return m.conversationContent(showNotice)
	}
	plan := layout.Plan(m.size, layout.Caps{}, layout.Shape{ComposerRows: 1})
	status := m.styles.presentation(m.status)
	statusLine := status.Mark + " " + status.Label
	if hint := m.quitHint(); hint != "" {
		statusLine += "  " + hint
	}
	if m.status.Cause != "" || m.status.State == Retrying {
		statusLine += "  " + status.Detail
	}
	if notice := m.localNotice(); showNotice && notice != "" {
		statusLine += "  " + notice
	}
	clip := func(text string) string { return ansi.Truncate(text, m.size.Width, "") }
	if plan.Tier == layout.TooSmall {
		rows := make([]string, m.size.Height)
		if plan.Notice.Height > 0 {
			rows[0] = clip(design.Text(m.styles.options.Language, "shell.too_small"))
		}
		rows[plan.Status.Y] = clip(statusLine)
		return strings.Join(rows, "\n")
	}
	if len(m.content.Blocks) == 0 || !m.status.State.active() {
		lines := strings.Split(strings.TrimSuffix(m.styles.ScreenStatus(m.status), "\n"), "\n")
		if hint := m.quitHint(); hint != "" {
			lines = append(lines, clip(hint))
		}
		if notice := m.localNotice(); showNotice && notice != "" {
			lines = append(lines, clip(m.styles.Muted.Render(notice)))
		}
		if len(lines) > m.size.Height {
			lines = lines[:m.size.Height]
			lines[len(lines)-1] = clip(statusLine)
		}
		return strings.Join(lines, "\n")
	}
	rows := make([]string, m.size.Height)
	name := "lina"
	if len(m.content.Agents) > 0 {
		name = sanitize.Display(m.content.Agents[0].Name)
	}
	rows[plan.Header.Y] = clip(m.styles.Title.Render(name))
	rows[plan.Status.Y] = clip(m.styles.Text.Render(statusLine))
	rows[plan.Composer.Y] = clip(m.styles.Muted.Render(design.Text(m.styles.options.Language, "shell.composer")))
	start := 0
	for i, block := range m.content.Blocks {
		if block.ID == m.content.Scroll.ID {
			start = i
			break
		}
	}
	y, end := plan.Conversation.Y, plan.Conversation.Y+plan.Conversation.Height
	for i := start; i < len(m.content.Blocks) && y < end; i++ {
		block := m.content.Blocks[i]
		cached := m.blocks[block.ID]
		if cached.stream == nil {
			continue
		}
		anchor := 0
		if block.ID == m.content.Scroll.ID && cached.stream.Len() > 0 {
			row := min(max(0, m.content.Scroll.Row), cached.stream.Len()-1)
			anchor = cached.stream.At(row).Offset
		}
		for _, row := range cached.stream.Window(anchor, end-y, render.Config{Width: m.size.Width}, nil) {
			rows[y] = clip(m.styles.Text.Render(row.Text))
			y++
		}
	}
	for i, line := range rows {
		rows[i] = m.styles.Canvas.Width(m.size.Width).Render(line)
	}
	return strings.Join(rows, "\n")
}
