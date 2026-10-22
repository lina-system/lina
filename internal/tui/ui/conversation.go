// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type conversationView struct {
	undo         []state.Draft
	scroll       int
	id           string
	agentMissing bool
	agentPending bool
	composer     textarea.Model
	pager        textPager
	cardsFocused bool
	selected     int
}

type textPager struct {
	running   bool
	selected  int
	runID     string
	next      *json.Number
	open      bool
	text      string
	cardID    string
	followEnd bool
	offset    int
	query     string
	searching bool
}

func newConversationView(styles Styles) conversationView {
	c := textarea.New()
	c.Prompt = ""
	c.ShowLineNumbers = false
	c.CharLimit = 16384
	c.MaxWidth, c.MaxHeight = 0, 6
	c.DynamicHeight = true
	c.MinHeight = 1
	c.MaxContentHeight = 16384
	c.SetStyles(textarea.Styles{Focused: textarea.StyleState{Text: styles.Text, Placeholder: styles.Muted}, Blurred: textarea.StyleState{Text: styles.Muted}})
	c.SetVirtualCursor(false)
	c.SetWidth(max(1, styles.options.Width))
	c.SetHeight(1)
	c.Focus()
	// Clipboard reads are never an application action; terminal PasteMsg owns paste.
	c.KeyMap.Paste.SetEnabled(false)
	c.KeyMap.CopySelection.SetEnabled(false)
	return conversationView{composer: c, selected: -1}
}

func (m *shellModel) hasCapability(name string) bool {
	for _, cap := range m.content.Capabilities {
		if cap.Name == name {
			return true
		}
	}
	return false
}

func (m *shellModel) syncComposer() {
	c := &m.conversation.composer
	if c.Value() != m.content.Draft.Text {
		c.SetValue(m.content.Draft.Text)
	}
	c.SetWidth(max(1, m.size.Width))

}

func (m *shellModel) editComposer(msg tea.Msg) tea.Cmd {
	if m.conversation.pager.open {
		return nil
	}
	m.syncComposer()
	c, command := m.conversation.composer.Update(msg)
	m.conversation.composer = c
	draft := m.content.Draft
	if draft.Text != c.Value() {
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
	var commands []state.Cmd
	m.content, commands = state.Reduce(m.content, msg)
	m.syncComposer()
	m.refreshBlocks()
	var effects []tea.Cmd
	for _, command := range commands {
		generation := m.generation
		effects = append(effects, func() tea.Msg { return shellEffect{Generation: generation, Message: command(context.Background())} })
	}
	return tea.Batch(effects...)
}

func (m *shellModel) conversationKey(msg tea.KeyMsg) tea.Cmd {
	key := term.FromTea(msg, m.events)
	class := term.Edit
	switch key.Name {
	case "enter", "esc", "ctrl+c", "ctrl+d", "tab", "shift+tab", "ctrl+o", "ctrl+w", "ctrl+r", "ctrl+p":
		class = term.Toggle
	}
	if !m.gate.Admit(key, class, m.now()) {
		return nil
	}
	if m.conversation.pager.open {
		return m.pagerKey(msg, key.Name)
	}
	if m.conversation.cardsFocused && (key.Name == "up" || key.Name == "down") {
		cards := m.resultCards()
		if len(cards) > 0 {
			if m.conversation.selected < 0 {
				m.conversation.selected = len(cards) - 1
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
	case "ctrl+z":
		if n := len(m.conversation.undo); n > 0 {
			draft := m.conversation.undo[n-1]
			m.conversation.undo = m.conversation.undo[:n-1]
			return m.reduceInput(state.EditDraft{Draft: draft})
		}
		return nil
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
		m.conversation.scroll += max(1, m.size.Height/2)
		return nil
	case "pgdown":
		m.conversation.scroll = max(0, m.conversation.scroll-max(1, m.size.Height/2))
		return nil
	case "ctrl+o":
		if card, ok := m.selectedCard(); ok && card.RunID != "" {
			return m.runningRequest(card.RunID, "open", nil)
		}
		m.openPager(m.cardText())
		if card, ok := m.selectedCard(); ok {
			m.conversation.pager.cardID = card.ID
		}
		return nil
	case "ctrl+w":
		m.openPager(m.topicText("waiting"))
		return nil
	case "ctrl+r":
		m.openRunning()
		return nil
	case "ctrl+p":
		m.openPager(m.topicText("progress"))
		return nil
	case "tab", "shift+tab":
		m.conversation.cardsFocused = !m.conversation.cardsFocused
		return nil
	case "enter":
		if m.conversation.cardsFocused {
			if card, ok := m.selectedCard(); ok && card.RunID != "" {
				return m.runningRequest(card.RunID, "open", nil)
			}
			m.openPager(m.cardText())
			return nil
		}
		if !m.mutationsAllowed() {
			return nil
		}
		text := strings.TrimSpace(m.content.Draft.Text)
		if text == "/help" {
			m.reduceInput(state.EditDraft{})
			text := func(id string) string { return design.Text(m.styles.options.Language, id) }
			m.openPager(strings.Join([]string{text("controls.help"),
				"/accounts  " + text("account.heading"), "/signin  " + text("sign_in_open"),
				"/memory  " + text("memory.heading"), "/agents  " + text("agents.heading"), "/channels  " + text("channel.heading"), "/talk  " + text("talk.heading"), "/stop  " + text("interrupt"),
			}, "\n"))
			return nil
		}
		if handled, command := m.talkCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
			return command
		}
		if handled, command := m.channelCommand(text); handled {
			m.reduceInput(state.EditDraft{})
			m.conversation.undo = nil
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
	case "alt+enter", "ctrl+j":
		return m.editComposer(tea.PasteMsg{Content: "\n"})
	}
	return m.editComposer(msg)
}

func (m *shellModel) conversationPlan() layout.Layout {
	m.syncComposer()
	return layout.Plan(m.size, layout.Caps{SidePane: true, Running: m.hasCapability("running"), Cards: m.hasCapability("waiting")}, layout.Shape{SidePane: m.conversation.pager.open, ComposerRows: m.conversation.composer.Height(), RunningCount: len(m.topicBlocks("running")), CardCount: len(m.topicBlocks("waiting")), CardRows: len(m.topicBlocks("waiting")), Attachments: len(m.content.Draft.Attachments) > 0})
}

func (m *shellModel) topicBlocks(topic string) []state.Block {
	var blocks []state.Block
	for _, b := range m.content.Blocks {
		if b.Topic == topic {
			if b.Card != nil {
				card := *b.Card
				for _, current := range m.content.Cards {
					if current.ID == card.ID {
						card = current
						break
					}
				}
				if card.Outcome != "" {
					continue
				}
				b.Text = m.cardSummary(card)
			}
			blocks = append(blocks, b)
		}
	}
	return blocks
}
func (m *shellModel) topicText(topic string) string {
	if !m.hasCapability(topic) {
		return design.Text(m.styles.options.Language, "capability_not_negotiated")
	}
	var lines []string
	for _, b := range m.topicBlocks(topic) {
		lines = append(lines, b.Text)
	}
	return strings.Join(lines, "\n")
}
func (m *shellModel) resultCards() []state.Block {
	var cards []state.Block
	for _, b := range m.content.Blocks {
		if b.Kind == protocol.KindResult {
			cards = append(cards, b)
		}
	}
	return cards
}
func (m *shellModel) selectedCard() (state.Block, bool) {
	cards := m.resultCards()
	if len(cards) > 0 {
		index := m.conversation.selected
		if index < 0 {
			index = len(cards) - 1
		}
		index = min(index, len(cards)-1)
		return cards[index], true
	}
	return state.Block{}, false
}
func (m *shellModel) cardText() string {
	if b, ok := m.selectedCard(); ok {
		if b.Body != "" {
			return b.Body
		}
		return b.Text
	}
	var lines []string
	for _, b := range m.content.Blocks {
		if strings.HasPrefix(b.Topic, "conversation:") || b.Local {
			lines = append(lines, b.Text)
		}
	}
	return strings.Join(lines, "\n\n")
}
func (m *shellModel) openPager(text string) {
	m.conversation.pager = textPager{open: true, text: sanitize.Display(text)}
}

func (m *shellModel) refreshOpenCard() {
	p := &m.conversation.pager
	if !p.open || p.cardID == "" {
		return
	}
	for _, card := range m.resultCards() {
		if card.ID == p.cardID {
			p.text = sanitize.Display(card.Body)
			if p.followEnd {
				p.offset = len(m.pagerLines())
			}
			return
		}
	}
}

func (m *shellModel) pagerKey(msg tea.KeyMsg, name string) tea.Cmd {
	p := &m.conversation.pager
	if p.running && !p.searching {
		rows := m.topicBlocks("running")
		if len(rows) > 0 {
			p.selected = min(p.selected, len(rows)-1)
			switch name {
			case "up", "k":
				p.selected = max(0, p.selected-1)
				m.refreshRunning()
				return nil
			case "down", "j":
				p.selected = min(len(rows)-1, p.selected+1)
				m.refreshRunning()
				return nil
			case "enter":
				return m.runningRequest(rows[p.selected].RunID, "open", nil)
			case "d":
				return m.runningRequest(rows[p.selected].RunID, "dismiss", nil)
			}
		}
	}
	if name == "ctrl+n" && p.next != nil && !p.searching {
		return m.runningRequest(p.runID, "open", p.next)
	}
	if name == "ctrl+d" {
		return tea.Quit
	}
	if name == "esc" {
		if p.searching {
			p.searching = false
		} else {
			p.open = false
		}
		return nil
	}
	if p.searching {
		switch name {
		case "enter":
			p.searching = false
			m.pagerFind()
		case "backspace":
			r := []rune(p.query)
			if len(r) > 0 {
				p.query = string(r[:len(r)-1])
			}
		default:
			p.query += sanitize.Display(msg.Key().Text)
		}
		return nil
	}
	p.followEnd = false
	switch name {
	case "q", "ctrl+o":
		p.open = false
	case "down", "j":
		p.offset++
	case "up", "k":
		p.offset = max(0, p.offset-1)
	case "pgdown", "space":
		p.offset += max(1, m.size.Height-2)
	case "pgup":
		p.offset = max(0, p.offset-max(1, m.size.Height-2))
	case "home":
		p.offset = 0
	case "end":
		p.followEnd = true
		p.offset = len(m.pagerLines())
	case "/":
		p.searching = true
		p.query = ""
	case "n":
		m.pagerFind()
	}
	return nil
}
func (m *shellModel) pagerLines() []string {
	width := m.size.Width
	if plan := m.conversationPlan(); plan.SidePane.Width > 0 {
		width = plan.SidePane.Width
	}
	stream := render.NewStream("pager", render.Config{Width: max(1, width)}, nil)
	stream.Seal(1, m.conversation.pager.text)
	var lines []string
	for _, row := range stream.Window(0, stream.Len(), render.Config{Width: max(1, width)}, nil) {
		lines = append(lines, row.Text)
	}
	return lines
}
func (m *shellModel) pagerFind() {
	p := &m.conversation.pager
	lines := m.pagerLines()
	if p.query == "" {
		return
	}
	for n := 1; n <= len(lines); n++ {
		i := (p.offset + n) % len(lines)
		if strings.Contains(lines[i], p.query) {
			p.offset = i
			return
		}
	}
}

func (m *shellModel) conversationContent(showNotice bool) string {
	plan := m.conversationPlan()
	rows := make([]string, m.size.Height)
	clip := func(s string) string { return ansi.Truncate(s, m.size.Width, "") }
	status := m.styles.presentation(m.status)
	statusLine := status.Mark + " " + status.Label
	if hint := m.quitHint(); hint != "" {
		statusLine += "  " + hint
	}
	if m.status.Cause != "" {
		statusLine += " " + status.Detail
	} else if notice := m.localNotice(); showNotice && notice != "" {
		statusLine += "  " + notice
	}
	rows[plan.Status.Y] = clip(statusLine + "  /help  Enter  Ctrl+D  /memory  /agents")
	if plan.Tier == layout.TooSmall {
		if plan.Notice.Height > 0 {
			rows[0] = clip(design.Text(m.styles.options.Language, "shell.too_small"))
		}
		return strings.Join(rows, "\n")
	}
	if m.conversation.pager.open && plan.SidePane.Height == 0 {
		p := &m.conversation.pager
		lines := m.pagerLines()
		p.offset = min(p.offset, max(0, len(lines)-(m.size.Height-1)))
		for y := 0; y < m.size.Height-1 && p.offset+y < len(lines); y++ {
			rows[y] = clip(lines[p.offset+y])
		}
		rows[plan.Status.Y] = clip("Esc  /  n  " + p.query)
		return strings.Join(rows, "\n")
	}
	name := "LINA"
	if m.conversation.agentMissing {
		name = design.Text(m.styles.options.Language, "agents.unavailable")
	}
	if !m.conversation.agentMissing {
		for _, a := range m.content.Agents {
			if a.AgentID == m.activeAgent() {
				name = a.Name
				break
			}
		}
	}
	rows[plan.Header.Y] = clip(m.styles.Title.Render(name))
	var transcript []render.StyledRow
	plainRow := func(text string) render.StyledRow { return render.StyledRow{Spans: []render.Span{{Text: text}}} }
	if !m.hasCapability("conversation") {
		code := "account.empty"
		if !m.accountsKnown {
			code = "account.loading"
		}
		for _, row := range m.accountRows {
			if row.Status == "ok" {
				code = "account.ready"
				break
			}
		}
		transcript = append(transcript, plainRow(design.Text(m.styles.options.Language, code)+"  /accounts"))
	}
	for _, b := range m.content.Blocks {
		if b.Topic == "core" || b.Topic == "running" || (b.Topic == "waiting" && b.Card == nil) || b.Topic == "progress" || b.Kind == protocol.KindTurnState {
			continue
		}
		cached := m.blocks[b.ID]
		if cached.stream == nil {
			continue
		}
		if b.Local || b.Speaker == protocol.ConversationSpeakerPerson {
			transcript = append(transcript, plainRow(design.Text(m.styles.options.Language, "person")))
		} else if b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindOutputDelta {
			transcript = append(transcript, plainRow(name))
		}
		if b.Kind == protocol.KindResult {
			transcript = append(transcript, plainRow(design.Text(m.styles.options.Language, "result")+"  "+sanitize.Display(b.Title)+"  Ctrl+O"))
		}
		transcript = append(transcript, cached.stream.Markdown(render.Config{Width: plan.Conversation.Width, Theme: m.styles.options.Theme}, m.styles.listBullet(), parseMarkdown)...)
	}
	m.conversation.scroll = min(m.conversation.scroll, max(0, len(transcript)-plan.Conversation.Height))
	start := max(0, len(transcript)-plan.Conversation.Height-m.conversation.scroll)
	for y, line := range transcript[start:] {
		if y >= plan.Conversation.Height {
			break
		}
		rows[plan.Conversation.Y+y] = clip(m.styles.markdownRow(line))
	}
	drawTopic := func(topic string, region layout.Rect) {
		if region.Height == 0 {
			return
		}
		blocks := m.topicBlocks(topic)
		label := design.Text(m.styles.options.Language, topic) + " " + strconv.Itoa(len(blocks))
		if topic == "waiting" && len(m.content.Cards) > 0 {
			label += "  F6"
		}
		for i := 0; i < region.Height && i < len(blocks); i++ {
			rows[region.Y+i] = clip(label + "  " + blocks[i].Text)
		}
		if len(blocks) > region.Height {
			rows[region.Y+region.Height-1] = clip(label + "  +" + strconv.Itoa(len(blocks)-region.Height+1))
		}
	}
	drawTopic("running", plan.Running)
	drawTopic("waiting", plan.Cards)
	if plan.Attachments.Height > 0 {
		rows[plan.Attachments.Y] = clip(strings.Join(m.content.Draft.Attachments, "  "))
	}
	m.conversation.composer.SetHeight(plan.Composer.Height)
	for i, line := range strings.Split(m.conversation.composer.View(), "\n") {
		if i >= plan.Composer.Height {
			break
		}
		rows[plan.Composer.Y+i] = clip(line)
	}
	if plan.SidePane.Height > 0 {
		p := &m.conversation.pager
		stream := render.NewStream("pager", render.Config{Width: plan.SidePane.Width}, nil)
		stream.Seal(1, p.text)
		p.offset = min(p.offset, max(0, stream.Len()-plan.SidePane.Height))
		anchor := 0
		if stream.Len() > 0 {
			anchor = stream.At(p.offset).Offset
		}
		lines := stream.Window(anchor, plan.SidePane.Height, render.Config{Width: plan.SidePane.Width}, nil)
		for y := 0; y < plan.SidePane.Height; y++ {
			left := ansi.Truncate(rows[plan.SidePane.Y+y], plan.Conversation.Width, "")
			left += strings.Repeat(" ", max(0, plan.Conversation.Width-ansi.StringWidth(left)))
			right := ""
			if y < len(lines) {
				right = lines[y].Text
			}
			rows[plan.SidePane.Y+y] = left + right
		}
		rows[plan.Status.Y] = clip("Esc  /  n  " + p.query)
	}
	return strings.Join(rows, "\n")
}
