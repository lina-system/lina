// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
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
}

type inputHistory struct {
	id     string
	draft  state.Draft
	column int // Recall starts on the first visual line of the saved draft.
}

type textPager struct {
	developer, developerBusy bool
	developerID              string
	lanes                    state.WorkerLanes
	laneNext                 *string
	transcript               bool
	running                  bool
	selected                 int
	runID                    string
	next                     *json.Number
	open                     bool
	text                     string
	cardID                   string
	followEnd                bool
	offset                   int
	query                    string
	searching                bool
}

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

func (m *shellModel) hasCapability(name string) bool {
	for _, cap := range m.content.Capabilities {
		if cap.Name == name {
			return true
		}
	}
	return false
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
	var effects []tea.Cmd
	for _, command := range commands {
		generation := m.generation
		effects = append(effects, func() tea.Msg { return shellEffect{Generation: generation, Message: command(context.Background())} })
	}
	return tea.Batch(effects...)
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
	case "enter", "esc", "ctrl+c", "ctrl+d", "tab", "shift+tab", "ctrl+o", "ctrl+t", "ctrl+w", "ctrl+r", "ctrl+p":
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
		text := strings.TrimSpace(m.content.Draft.Text)
		if text == "/help" {
			m.reduceInput(state.EditDraft{})
			m.openPager(m.shortcutSheet())
			return nil
		}
		if !m.mutationsAllowed() {
			return nil
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
	case "shift+enter", "alt+enter", "ctrl+j":
		return m.editComposer(tea.PasteMsg{Content: "\n"})
	}
	return m.editComposer(msg)
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

func (m *shellModel) conversationPlan() layout.Layout {
	m.syncComposer()
	activityRows := 0
	if m.activityRequested() {
		activityRows = len(strings.Split(m.activityText(), "\n"))
	}
	return layout.Plan(m.size, layout.Caps{SidePane: true, Running: m.hasCapability("running"), Cards: m.hasCapability("waiting")}, layout.Shape{ActivityRows: activityRows, SidePane: m.conversation.pager.open, ComposerRows: m.conversation.composer.Height(), RunningCount: len(m.topicBlocks("running")), CardCount: len(m.topicBlocks("waiting")), CardRows: len(m.topicBlocks("waiting")), Attachments: len(m.content.Draft.Attachments) > 0})
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

func conversationBlock(b state.Block) bool {
	return b.Topic != "core" && b.Topic != "running" && (b.Topic != "waiting" || b.Card != nil) && b.Topic != "progress" && b.Kind != protocol.KindTurnState && b.Kind != protocol.KindToolActivity
}

func (m *shellModel) conversationName() string {
	if m.conversation.agentMissing {
		return design.Text(m.styles.options.Language, "agents.unavailable")
	}
	for _, a := range m.content.Agents {
		if a.AgentID == m.activeAgent() {
			return sanitize.Display(a.Name)
		}
	}
	return "LINA"
}

func (m *shellModel) speakerName(b state.Block) string {
	if b.Local || b.Speaker == protocol.ConversationSpeakerPerson {
		return design.Text(m.styles.options.Language, "person")
	}
	if b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindOutputDelta {
		return m.conversationName()
	}
	return ""
}

func (m *shellModel) pagerText() string {
	if !m.conversation.pager.transcript {
		return m.conversation.pager.text
	}
	// Read the current projection, without retaining another history copy that
	// could outlive authoritative replacements, revocation or a focus change.
	var messages []string
	for _, b := range m.content.Blocks {
		if !conversationBlock(b) {
			continue
		}
		text := b.Text
		if b.Kind == protocol.KindResult && !b.InlineReply {
			text = design.Text(m.styles.options.Language, "result") + "  " + b.Title + "\n" + text
		}
		if speaker := m.speakerName(b); speaker != "" {
			text = speaker + "\n" + text
		}
		messages = append(messages, text)
	}
	return sanitize.Display(strings.Join(messages, "\n\n"))
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
	if p.developer && !p.searching {
		if handled, cmd := m.developerKey(name); handled {
			return cmd
		}
	}
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
	stream.Seal(1, m.pagerText())
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
	rows[plan.Status.Y] = m.conversationFooter(showNotice)
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
		return strings.Join(rows, "\n")
	}
	name := m.conversationName()
	rows[plan.Header.Y] = clip(m.styles.Title.Render(name))
	var transcript []render.StyledRow
	var anchors []conversationAnchor
	mutedRows := make(map[int]bool)
	plainRow := func(text string) render.StyledRow { return render.StyledRow{Spans: []render.Span{{Text: text}}} }
	if !m.hasCapability("conversation") {
		code := "account.empty"
		if !m.accountsKnown {
			code = "account.loading"
			if m.accounts.code != "" {
				code = m.accounts.code
			}
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
		if !conversationBlock(b) {
			continue
		}
		cached := m.blocks[b.ID]
		if cached.stream == nil {
			continue
		}
		// The account placeholder has no retained conversation identity.
		for len(anchors) < len(transcript) {
			anchors = append(anchors, conversationAnchor{})
		}
		blockStart := len(transcript)
		if speaker := m.speakerName(b); speaker != "" {
			transcript = append(transcript, plainRow(speaker))
		}
		if b.Kind == protocol.KindResult && !b.InlineReply {
			transcript = append(transcript, plainRow(design.Text(m.styles.options.Language, "result")+"  "+sanitize.Display(b.Title)+"  Ctrl+O"))
		}
		transcript = append(transcript, cached.stream.Markdown(render.Config{Width: plan.Conversation.Width, Theme: m.styles.options.Theme}, m.styles.listBullet(), parseMarkdown)...)
		if elapsed, ok := m.content.Worked[b.ID]; ok {
			mutedRows[len(transcript)] = true
			transcript = append(transcript, plainRow(fmt.Sprintf(design.Text(m.styles.options.Language, "activity.worked_for"), formatElapsed(elapsed))))
		}
		if b.InlineReply {
			transcript = append(transcript, plainRow(design.Text(m.styles.options.Language, "reply.open_pager")))
		}
		for row := 0; row < len(transcript)-blockStart; row++ {
			anchors = append(anchors, conversationAnchor{block: b.ID, row: row})
		}
	}
	start := m.conversationStart(anchors, len(transcript), plan.Conversation.Height)
	m.conversation.viewport.overflow = len(transcript) > plan.Conversation.Height
	rows[plan.Status.Y] = m.conversationFooter(showNotice)
	for y, line := range transcript[start:] {
		if y >= plan.Conversation.Height {
			break
		}
		rows[plan.Conversation.Y+y] = clip(m.styles.markdownRow(line))
		if mutedRows[start+y] {
			rows[plan.Conversation.Y+y] = m.styles.Muted.Render(render.Truncate(line.Spans[0].Text, plan.Conversation.Width, render.Unicode{}))
		}
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
	if plan.CombinedWork {
		rows[plan.Running.Y] = fmt.Sprintf("%s %d  %s %d", design.Text(m.styles.options.Language, "running"), plan.RunningCount, design.Text(m.styles.options.Language, "waiting"), plan.CardCount)
	} else {
		drawTopic("running", plan.Running)
		drawTopic("waiting", plan.Cards)
	}
	if plan.Attachments.Height > 0 {
		rows[plan.Attachments.Y] = clip(strings.Join(m.content.Draft.Attachments, "  "))
	}
	if plan.Activity.Height > 0 {
		for i, line := range strings.Split(m.activityText(), "\n") {
			rows[plan.Activity.Y+i] = m.styles.Muted.Render(line)
		}
	}
	if plan.ComposerRule.Height > 0 {
		rule := m.styles.tokens.GlyphUnicodeRule
		if m.styles.options.ASCII {
			rule = m.styles.tokens.GlyphAsciiRule
		}
		rows[plan.ComposerRule.Y] = m.styles.Line.Render(strings.Repeat(rule, plan.ComposerRule.Width))
	}
	m.conversation.composer.SetHeight(plan.Composer.Height)
	for i, line := range strings.Split(m.conversation.composer.View(), "\n") {
		if i >= plan.Composer.Height {
			break
		}
		// Keep the prompt on the first visible row even when the draft scrolls.
		prefix := strings.Repeat(" ", m.styles.composerPromptWidth())
		if i == 0 {
			prefix = m.styles.Accent.Render(m.styles.composerPrompt())
		}
		rows[plan.Composer.Y+i] = clip(prefix + line)
	}
	if plan.SidePane.Height > 0 {
		p := &m.conversation.pager
		stream := render.NewStream("pager", render.Config{Width: plan.SidePane.Width}, nil)
		stream.Seal(1, m.pagerText())
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
	}
	return strings.Join(rows, "\n")
}
