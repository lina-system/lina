// SPDX-License-Identifier: Apache-2.0

package ui

import (
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

type textPager struct {
	board                    *boardView
	records                  *recordPager
	recordID                 string
	tool                     bool
	developer, developerBusy bool
	developerID              string
	lanes                    state.WorkerLanes
	laneNext                 *string
	transcript               bool
	running                  bool
	progress                 bool
	waiting                  bool
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
	case "enter", "esc", "ctrl+c", "ctrl+d", "tab", "shift+tab", "ctrl+o", "ctrl+t", "ctrl+w", "ctrl+r", "ctrl+p", "f6":
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
		if summary := m.toolSummary(); summary != "" {
			m.openPager(summary)
			m.conversation.pager.tool = true
			return nil
		}
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
		m.conversation.pager.waiting = true
		return m.waitingRequest()
	case "ctrl+r":
		m.openRunning()
		return nil
	case "ctrl+p":
		m.openPager(m.topicText("progress"))
		m.conversation.pager.progress = true
		return m.subscribeSnapshot("progress")
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

func (m *shellModel) conversationPlan() layout.Layout {
	m.syncComposer()
	activityRows := 0
	if m.activityRequested() {
		activityRows = len(strings.Split(m.activityText(), "\n"))
	}
	cardRows := len(m.topicBlocks("waiting"))
	cardCount := cardRows
	if summary := m.homeWaiting(); summary != nil {
		cardRows = len(summary.Lines)
		cardCount = cardRows + summary.More
	}
	return layout.Plan(m.size, layout.Caps{SidePane: true, Running: m.hasCapability("running"), Cards: m.hasCapability("waiting")}, layout.Shape{ActivityRows: activityRows, SidePane: m.conversation.pager.open, ComposerRows: m.conversation.composer.Height(), RunningCount: len(m.topicBlocks("running")), CardCount: cardCount, CardRows: cardRows, Attachments: len(m.content.Draft.Attachments) > 0})
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
	if p.open && p.tool {
		p.text = m.toolSummary()
		if p.text == "" {
			*p = textPager{}
		}
		return
	}
	if !p.open || p.cardID == "" {
		return
	}
	for _, card := range m.resultCards() {
		if card.ID == p.cardID {
			p.text = sanitize.Display(resultText(card))
			if p.followEnd {
				p.offset = len(m.pagerLines())
			}
			return
		}
	}
}

// Results carry complete replacement previews. Only a live update during a
// turn opens a streaming projection; history and earlier results remain sealed.
func (m *shellModel) refreshResultStreams(message state.Msg) {
	live, isUpdate := message.(state.ItemDone)
	next := make(map[string]shellBlock)
	for _, card := range m.resultCards() {
		text := resultText(card)
		cached, ok := m.resultStreams[card.ID]
		done := !ok || cached.done
		if isUpdate && live.Block.ID == card.ID && (!ok || cached.text != text) {
			done = !m.content.TurnActive
		}
		if !m.content.TurnActive {
			done = true
		}
		if !ok || cached.text != text || cached.done != done {
			revision := cached.revision + 1
			stream := cached.stream
			if !ok || cached.done || !strings.HasPrefix(text, cached.text) {
				stream = render.NewStream(card.ID, render.Config{Width: max(1, m.size.Width)}, nil)
				if !done {
					stream.Append(revision, text)
				}
			} else if !done {
				stream.Append(revision, text[len(cached.text):])
			}
			if done {
				stream.Seal(revision, text)
			}
			cached = shellBlock{text, revision, done, stream}
		}
		next[card.ID] = cached
	}
	m.resultStreams = next
}

func (m *shellModel) pagerKey(msg tea.KeyMsg, name string) tea.Cmd {
	p := &m.conversation.pager
	if p.board != nil && !p.searching {
		if handled, cmd := m.boardKey(name); handled {
			return cmd
		}
	}
	if p.records != nil && !p.searching {
		if handled, cmd := m.recordsKey(name); handled {
			return cmd
		}
	}
	if p.developer && !p.searching {
		if handled, cmd := m.developerKey(name); handled {
			return cmd
		}
	}
	if p.waiting && !p.searching && name == "o" {
		if m.waitingOrder == "lightest" {
			m.waitingOrder = "priority"
		} else {
			m.waitingOrder = "lightest"
		}
		return m.waitingRequest()
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
			case "s":
				_, cmd := m.ownerCommand("/stop " + rows[p.selected].RunID)
				return cmd
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
func (m *shellModel) pagerRows() []render.StyledRow {
	width := m.size.Width
	if plan := m.conversationPlan(); plan.SidePane.Width > 0 {
		width = plan.SidePane.Width
	}
	config := render.Config{Width: max(1, width)}
	stream := m.resultStreams[m.conversation.pager.cardID].stream
	if stream == nil {
		stream = render.NewStream("pager", config, nil)
		stream.Seal(1, m.pagerText())
	}
	return stream.CodePreview(config)
}

func (m *shellModel) pagerLines() []string {
	var lines []string
	for _, row := range m.pagerRows() {
		var line strings.Builder
		for _, span := range row.Spans {
			line.WriteString(span.Text)
		}
		lines = append(lines, line.String())
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
		lines := m.pagerRows()
		p.offset = min(p.offset, max(0, len(lines)-(m.size.Height-1)))
		for y := 0; y < m.size.Height-1 && p.offset+y < len(lines); y++ {
			rows[y] = clip(m.styles.markdownRow(lines[p.offset+y]))
		}
		return strings.Join(rows, "\n")
	}
	name := m.conversationName()
	rows[plan.Header.Y] = clip(m.styles.Title.Render(name))
	type transcriptRow struct {
		body   render.StyledRow
		prefix string
		person bool
		muted  bool
	}
	var transcript []transcriptRow
	var anchors []conversationAnchor
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
		transcript = append(transcript, transcriptRow{body: plainRow(design.Text(m.styles.options.Language, code) + "  /accounts")})
	}
	groups := m.toolGroups()
	appendTools := func(anchor string) {
		for _, group := range groups {
			if group.calls[0].Anchor != anchor {
				continue
			}
			for _, row := range m.toolGroupRows(group, plan.Conversation.Width) {
				transcript = append(transcript, transcriptRow{body: plainRow(row), muted: true})
			}
		}
	}
	appendTools("")
	hasItems := false
	for _, b := range m.content.Blocks {
		if !conversationBlock(b) {
			continue
		}
		hasItems = true
		cached := m.blocks[b.ID]
		if cached.stream == nil {
			continue
		}
		person := b.Local || b.Speaker == protocol.ConversationSpeakerPerson
		prefix := ""
		if person || b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindOutputDelta {
			prefix = m.styles.speakerPrefix(person, name, plan.Conversation.Width)
		}
		indent := strings.Repeat(" ", (render.Unicode{}).Width(prefix))
		stream := cached.stream
		if b.InlineReply && m.resultStreams[b.ID].stream != nil {
			stream = m.resultStreams[b.ID].stream
		}
		body := stream.Markdown(render.Config{Width: plan.Conversation.Width - len(indent), Theme: m.styles.options.Theme}, m.styles.listBullet(), parseMarkdown)
		// Paragraph spacing belongs inside a message; boundaries get exactly one
		// section gap even if the provider ended a message with empty lines.
		blank := func(row render.StyledRow) bool {
			for _, span := range row.Spans {
				if span.Style.Code || strings.TrimSpace(span.Text) != "" {
					return false
				}
			}
			return true
		}
		for len(body) > 0 && blank(body[0]) {
			body = body[1:]
		}
		for len(body) > 0 && blank(body[len(body)-1]) {
			body = body[:len(body)-1]
		}
		// Markdown owns these rows. Appending presentation metadata to a
		// shortened slice must not overwrite the cache's trimmed boundary rows.
		body = slices.Clone(body)
		if b.Kind == protocol.KindResult && !b.InlineReply {
			body = append([]render.StyledRow{plainRow(design.Text(m.styles.options.Language, "result") + "  " + sanitize.Display(b.Title) + "  Ctrl+O")}, body...)
		}
		toolRowsAt := len(body)
		workedRow := -1
		if elapsed, ok := m.content.Worked[b.ID]; ok {
			workedRow = len(body)
			body = append(body, plainRow(fmt.Sprintf(design.Text(m.styles.options.Language, "activity.worked_for"), formatElapsed(elapsed))))
		}
		if b.InlineReply {
			body = append(body, plainRow(design.Text(m.styles.options.Language, "reply.open_pager")))
		}
		if len(body) == 0 && !slices.ContainsFunc(groups, func(group toolGroup) bool { return group.calls[0].Anchor == b.ID }) {
			continue
		}
		// The account placeholder has no retained conversation identity.
		for len(anchors) < len(transcript) {
			anchors = append(anchors, conversationAnchor{})
		}
		if len(transcript) > 0 {
			transcript = append(transcript, make([]transcriptRow, m.styles.tokens.SpaceSection.Cells)...)
			// Anchor separator rows to the following block without shifting
			// its body row identities when older messages are inserted above it.
			for row := -m.styles.tokens.SpaceSection.Cells; row < 0; row++ {
				anchors = append(anchors, conversationAnchor{block: b.ID, row: row})
			}
		}
		blockStart := len(transcript)
		if toolRowsAt == 0 {
			appendTools(b.ID)
		}
		for i, row := range body {
			lead := indent
			if i == 0 {
				lead = prefix
			}
			if i == workedRow {
				lead = ""
			}
			transcript = append(transcript, transcriptRow{body: row, prefix: lead, person: person, muted: i == workedRow})
			if i+1 == toolRowsAt {
				appendTools(b.ID)
			}
		}
		for row := 0; row < len(transcript)-blockStart; row++ {
			anchors = append(anchors, conversationAnchor{block: b.ID, row: row})
		}
	}
	// Starter hints are local presentation, never reducer blocks or Core items.
	// Wait for history so reconnecting to an existing conversation cannot flash
	// a first-run screen. Account setup can show hints before chat is negotiated.
	_, historyKnown := m.content.Topics["conversation:"+m.conversation.id]
	if !hasItems && !m.conversation.agentMissing && !m.conversation.agentPending && (historyKnown || !m.hasCapability("conversation")) {
		status := m.styles.presentation(m.status)
		rows[plan.Header.Y] = clip(m.styles.wordmark() + "  " + m.styles.Title.Render(name) + "  " + m.styles.Text.Render(status.Label))
		var hints []transcriptRow
		for _, id := range []string{"conversation.start.memory", "conversation.start.voice", "conversation.start.task"} {
			hints = append(hints, transcriptRow{body: plainRow(design.Text(m.styles.options.Language, id)), muted: true})
		}
		transcript = append(hints, transcript...)
	}
	start := m.conversationStart(anchors, len(transcript), plan.Conversation.Height)
	m.conversation.viewport.overflow = len(transcript) > plan.Conversation.Height
	rows[plan.Status.Y] = m.conversationFooter(showNotice)
	for y, line := range transcript[start:] {
		if y >= plan.Conversation.Height {
			break
		}
		styles := m.styles
		if line.person {
			styles.Text = styles.Muted
		}
		prefix := ""
		if line.prefix != "" {
			prefix = m.styles.Accent.Render(line.prefix)
		}
		rows[plan.Conversation.Y+y] = clip(prefix + styles.markdownRow(line.body))
		if line.muted {
			width := plan.Conversation.Width - (render.Unicode{}).Width(line.prefix)
			rows[plan.Conversation.Y+y] = clip(prefix + m.styles.Muted.Render(render.Truncate(line.body.Spans[0].Text, width, render.Unicode{})))
		}
	}
	drawTopic := func(topic string, region layout.Rect) {
		if region.Height == 0 {
			return
		}
		if topic == "waiting" {
			if summary := m.homeWaiting(); summary != nil {
				total := len(summary.Lines) + summary.More
				label := design.Text(m.styles.options.Language, "waiting") + " " + strconv.Itoa(total)
				shown := min(region.Height, len(summary.Lines))
				for i := 0; i < max(1, shown); i++ {
					line := label
					if i < shown {
						line += "  " + summary.Lines[i]
					}
					if i == max(1, shown)-1 && total > shown {
						suffix := "  " + fmt.Sprintf(design.Text(m.styles.options.Language, "progress.more"), strconv.Itoa(total-shown))
						line = render.Truncate(line, max(0, region.Width-(render.Unicode{}).Width(suffix)), render.Unicode{}) + suffix
					}
					rows[region.Y+i] = clip(line)
				}
				return
			}
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
		lines := m.pagerRows()
		p.offset = min(p.offset, max(0, len(lines)-plan.SidePane.Height))
		lines = lines[p.offset:]
		for y := 0; y < plan.SidePane.Height; y++ {
			left := ansi.Truncate(rows[plan.SidePane.Y+y], plan.Conversation.Width, "")
			left += strings.Repeat(" ", max(0, plan.Conversation.Width-ansi.StringWidth(left)))
			right := ""
			if y < len(lines) {
				right = m.styles.markdownRow(lines[y])
			}
			rows[plan.SidePane.Y+y] = left + right
		}
	}
	return strings.Join(rows, "\n")
}
