// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"
	"time"

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

// These are local projections, not another wire format. The connection owner
// tags every asynchronous result; a replaced connection cannot update the UI.
type shellBegin struct{ Generation uint64 }
type shellConnection struct {
	Generation uint64
	Status     ConnectionStatus
}
type shellData struct {
	Generation uint64
	Message    state.Msg
}
type shellEffect struct {
	Generation uint64
	Message    state.Msg
}
type shellSubscription struct {
	Generation uint64
	Request    protocol.Subscribe
}

type shellBlock struct {
	text     string
	revision uint64
	done     bool
	stream   *render.Stream
}

type shellModel struct {
	generation    uint64
	status        ConnectionStatus
	content       state.State
	styles        Styles
	size          layout.Size
	gate          term.Gate
	events        bool
	now           func() time.Time
	blocks        map[string]shellBlock
	resyncPending map[string]protocol.StreamCursor
	resyncSeen    map[resyncSignal]protocol.StreamCursor
	conversation  conversationView
}

func newShell(options Options, now func() time.Time) *shellModel {
	if now == nil {
		now = time.Now
	}
	styles := NewStyles(options)
	m := &shellModel{generation: 1, status: ConnectionStatus{State: Connecting}, styles: styles,
		size: layout.Size{Width: styles.options.Width, Height: 24}, now: now, blocks: make(map[string]shellBlock),
		resyncPending: make(map[string]protocol.StreamCursor), resyncSeen: make(map[resyncSignal]protocol.StreamCursor)}
	m.conversation = newConversationView(styles)
	return m
}

func (m *shellModel) Init() tea.Cmd          { return nil }
func (m *shellModel) mutationsAllowed() bool { return m.status.State.CanMutate() }

func (m *shellModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.status.State == Quit {
		return m, nil
	}
	switch msg := message.(type) {
	case shellBegin:
		if msg.Generation > m.generation {
			m.generation = msg.Generation
			m.content, _ = state.Reduce(m.content, state.ConnectionLost{})
			m.refreshBlocks()
			m.status = ConnectionStatus{State: Retrying}
			m.resyncSeen = make(map[resyncSignal]protocol.StreamCursor)
		}
	case shellConnection:
		if msg.Generation == m.generation {
			m.status = msg.Status
			if m.status.State == Live && (m.hasGap() || len(m.resyncPending) > 0) {
				m.status.State = Resyncing
			}
		}
	case shellData:
		if msg.Generation != m.generation {
			return m, nil
		}
		var projected *streamProjection
		if frame, ok := msg.Message.(state.Frame); ok {
			var decoder protocol.ClientDecoder
			envelope, err := decoder.Decode(frame.Data)
			if err == nil && envelope != nil {
				if envelope.Error != nil {
					status := Refused
					if envelope.Error.Code == protocol.ErrorUnavailable {
						status = Recovering
					}
					if envelope.Error.Code == protocol.ErrorMalformed {
						status = Failed
					}
					m.status = ConnectionStatus{State: status, Cause: envelope.Error.Code}
				}
				projection, err := projectStream(*envelope)
				if err != nil {
					m.status = ConnectionStatus{State: Failed, Cause: protocol.ErrorMalformed}
					return m, nil
				}
				if projection.topic != "" {
					if projection.resync {
						return m, m.markResync(projection)
					}
					if floor, pending := m.resyncPending[projection.topic]; pending && (!projection.snapshot || compareStreamCursor(projection.cursor, floor) < 0) {
						return m, nil
					}
					msg.Message = projection.message
					if snapshot, ok := msg.Message.(state.Snapshot); ok {
						snapshot.ClientTime = m.now()
						msg.Message = snapshot
					}
					projected = &projection
				}
			}
		}
		var commands []state.Cmd
		m.content, commands = state.Reduce(m.content, msg.Message)
		m.refreshBlocks()
		for _, block := range m.content.Blocks {
			if block.Kind == protocol.KindTurnState && block.TurnKnown {
				m.content, _ = state.Reduce(m.content, state.Turn{Active: block.TurnActive})
			}
		}
		if projected != nil && m.content.Topics[projected.topic].OutOfSync {
			if floor, exists := m.resyncPending[projected.topic]; !exists || compareStreamCursor(projected.cursor, floor) > 0 {
				m.resyncPending[projected.topic] = projected.cursor
			}
		}
		if projected != nil && projected.snapshot {
			topic := m.content.Topics[projected.topic]
			seq, _ := streamNumber(projected.cursor.Seq)
			if m.content.Epoch == projected.cursor.Epoch && topic.Cursor >= seq && !topic.OutOfSync {
				delete(m.resyncPending, projected.topic)
			}
			if len(m.resyncPending) == 0 && !m.hasGap() && m.status.State == Resyncing {
				m.status = ConnectionStatus{State: Live}
			}
		}
		if m.hasGap() {
			m.status = ConnectionStatus{State: Resyncing}
		}
		var effects []tea.Cmd
		for _, command := range commands {
			generation := m.generation
			effects = append(effects, func() tea.Msg {
				return shellEffect{Generation: generation, Message: command(context.Background())}
			})
		}
		return m, tea.Batch(effects...)
	case shellEffect:
		if msg.Generation != m.generation {
			return m, nil
		}
		if request, ok := msg.Message.(state.ResyncRequest); ok && request.Gen == m.content.Gen {
			return m, m.subscribeSnapshot(request.Topic)
		}
	case tea.WindowSizeMsg:
		m.size = layout.Size{Width: max(0, msg.Width), Height: max(0, msg.Height)}
		options := m.styles.options
		options.Width = max(1, msg.Width)
		m.styles = NewStyles(options)
	case tea.KeyboardEnhancementsMsg:
		m.events = msg.SupportsEventTypes()
	case tea.PasteMsg:
		if m.hasCapability("conversation") {
			return m, m.editComposer(tea.PasteMsg{Content: sanitize.Display(msg.Content)})
		}
	case tea.KeyMsg:
		if m.hasCapability("conversation") {
			return m, m.conversationKey(msg)
		}
		key := term.FromTea(msg, m.events)
		if !m.gate.Admit(key, term.Toggle, m.now()) {
			return m, nil
		}
		switch key.Name {
		case "ctrl+c":
			if m.content.Draft.Text != "" || len(m.content.Draft.Attachments) != 0 {
				m.content, _ = state.Reduce(m.content, state.EditDraft{})
				return m, nil
			}
			fallthrough
		case "q", "ctrl+d":
			m.status = ConnectionStatus{State: Quit}
			return m, tea.Quit
		}
	}
	return m, nil
}

// Index only changed blocks. View then requests a bounded window from the
// indexed stream rather than reflowing the entire history on each status tick.
func (m *shellModel) refreshBlocks() {
	next := make(map[string]shellBlock, len(m.content.Blocks))
	for _, block := range m.content.Blocks {
		cached, ok := m.blocks[block.ID]
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
	if m.hasCapability("conversation") && m.status.State.active() {
		plan := m.conversationPlan()
		if plan.Tier != layout.TooSmall && !m.conversation.pager.open {
			view.Cursor = m.conversation.composer.Cursor()
			if view.Cursor != nil {
				view.Cursor.Position.Y += plan.Composer.Y
			}
		}
	}
	return view
}

// Notices stay outside history. A capability notice is no longer current when
// a later welcome grants a different set of lists.
func (m *shellModel) localNotice() string {
	for i := len(m.content.Notices) - 1; i >= 0; i-- {
		code := m.content.Notices[i].Code
		switch code {
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
	if m.size.Width <= 0 || m.size.Height <= 0 {
		return ""
	}
	if m.hasCapability("conversation") && m.status.State.active() {
		return m.conversationContent(showNotice)
	}
	plan := layout.Plan(m.size, layout.Caps{}, layout.Shape{ComposerRows: 1})
	status := m.styles.presentation(m.status)
	statusLine := status.Mark + " " + status.Label
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
