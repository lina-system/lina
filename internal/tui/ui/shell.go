// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
	waitingOrder  string
	activityFrame uint64
	cardScroll    int
	cardRead      cardReading
	cardRequests  map[string]state.CardAnswerRequest
	focused       bool
	cardGate      term.Gate
	cardField     int
	generation    uint64
	status        ConnectionStatus
	content       state.State
	styles        Styles
	size          layout.Size
	gate          term.Gate
	events        bool
	disambiguated bool
	now           func() time.Time
	quitDeadline  time.Time
	blocks        map[string]shellBlock
	resultStreams map[string]shellBlock
	resyncPending map[string]protocol.StreamCursor
	resyncSeen    map[resyncSignal]protocol.StreamCursor
	conversation  conversationView
	talk          talkView
	channels      channelView
	web           webView
	accounts      accountView
	accountRows   []protocol.AccountSummary
	accountsKnown bool
	memoryPage    *protocol.MemoryRequest
}

func newShell(options Options, now func() time.Time) *shellModel {
	if now == nil {
		now = time.Now
	}
	styles := NewStyles(options)
	m := &shellModel{cardRequests: make(map[string]state.CardAnswerRequest), focused: true, generation: 1, status: ConnectionStatus{State: Connecting}, styles: styles,
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
	case quitHintExpired:
		if !m.now().Before(m.quitDeadline) {
			m.quitDeadline = time.Time{}
		}
	case activityTick:
		if m.activityInterval() > 0 {
			m.content, _ = state.Reduce(m.content, state.Tick{Now: time.Time(msg)})
			if !m.staticActivity() {
				m.activityFrame++
			}
		}

	case shellBegin:
		m.beginGeneration(msg)
	case shellConnection:
		m.applyConnection(msg)
	case shellData:
		if msg.Generation != m.generation {
			return m, nil
		}
		return m, m.applyData(msg.Message)
	case shellEffect:
		if msg.Generation != m.generation {
			return m, nil
		}
		if bell, ok := msg.Message.(state.CardBell); ok {
			return m, m.cardNotification(bell)
		}
		if request, ok := msg.Message.(state.ResyncRequest); ok && request.Gen == m.content.Gen {
			return m, m.subscribeSnapshot(request.Topic)
		}
	case tea.FocusMsg:
		m.focused = true
	case tea.BlurMsg:
		m.focused = false
	case tea.WindowSizeMsg:
		if m.size.Width != max(0, msg.Width) || m.size.Height != max(0, msg.Height) {
			m.resetCardReading()
		}
		m.size = layout.Size{Width: max(0, msg.Width), Height: max(0, msg.Height)}
		options := m.styles.options
		options.Width = max(1, msg.Width)
		m.styles = NewStyles(options)
		m.refreshDeveloper()
		m.refreshBoard()
	case tea.KeyboardEnhancementsMsg:
		m.events = msg.SupportsEventTypes()
		m.disambiguated = msg.SupportsKeyDisambiguation()
	case tea.MouseWheelMsg:
		m.conversationWheel(msg)
	case tea.PasteMsg:
		if m.web.open {
			m.webPaste(msg.Content)
			return m, nil
		}
		if m.content.CardView.ID != "" {
			return m, m.cardPaste(msg.Content)
		}
		if m.talk.open {
			return m, nil
		}
		if m.channels.open {
			m.channelPaste(msg.Content)
			return m, nil
		}
		if m.accounts.open {
			m.accountPaste(msg.Content)
			return m, nil
		}
		if m.hasComposer() {
			return m, m.editComposer(tea.PasteMsg{Content: sanitize.Display(msg.Content)})
		}
	case tea.KeyMsg:
		if m.web.open {
			return m, m.webKey(msg)
		}
		if m.talk.open {
			return m, m.talkKey(msg)
		}
		if m.channels.open {
			return m, m.channelKey(msg)
		}
		if m.accounts.open {
			return m, m.accountKey(msg)
		}
		key := term.FromTea(msg, m.events)
		if card, open := m.content.CurrentCard(); open && card.Kind == protocol.KindQuestion && msg.Key().Text != "" && msg.Key().Mod & ^tea.ModShift == 0 {
			if !m.cardGate.Admit(key, term.Edit, m.now()) {
				return m, nil
			}
			return m, m.cardPaste(msg.Key().Text)
		}
		if !m.conversation.pager.open {
			if cmd, handled := m.cardKey(key); handled {
				return m, cmd
			}
		}
		if m.hasComposer() {
			return m, m.conversationKey(msg)
		}
		if !m.gate.Admit(key, term.Toggle, m.now()) {
			return m, nil
		}
		switch key.Name {
		case "ctrl+c":
			return m, m.controlC()
		case "q", "ctrl+d":
			m.status = ConnectionStatus{State: Quit}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *shellModel) beginGeneration(msg shellBegin) {
	if msg.Generation > m.generation {
		m.generation = msg.Generation
		m.conversation.pager = textPager{}
		m.conversation.workFocused = false
		m.talk = talkView{attempt: m.talk.attempt + 1}
		m.channels = channelView{attempt: m.channels.attempt + 1}
		m.web = webView{attempt: m.web.attempt + 1}
		m.accounts = accountView{}
		m.accountRows = nil
		m.accountsKnown = false
		m.memoryPage = nil
		m.content, _ = state.Reduce(m.content, state.ConnectionLost{})
		m.conversation.agentPending = m.conversation.id != ""
		// Retain the visible window, but rediscover its subscriptions only
		// after the selected agent is validated on the new connection.
		for topic := range m.content.Topics {
			if m.conversation.agentPending && (strings.HasPrefix(topic, "conversation:") || strings.HasPrefix(topic, "results:")) {
				delete(m.content.Topics, topic)
				delete(m.resyncPending, topic)
			}
		}
		m.refreshBlocks()
		m.status = ConnectionStatus{State: Retrying}
		m.resyncSeen = make(map[resyncSignal]protocol.StreamCursor)
		m.cardRequests = make(map[string]state.CardAnswerRequest)
		m.content, _ = state.Reduce(m.content, state.ResetCardRequests{})
		m.resetCardReading()
	}
}

func (m *shellModel) applyConnection(msg shellConnection) {
	if msg.Generation == m.generation {
		m.status = msg.Status
		if m.status.State == Live && m.conversation.agentPending && m.conversation.id == "" {
			// Welcome alone does not supply a valid input destination.
			m.status.State = Connecting
		} else if m.status.State == Live && (m.conversation.agentPending || m.hasGap() || len(m.resyncPending) > 0) {
			m.status.State = Resyncing
		}
	}
}

// projectFrame handles replies and stream admission before data reaches the reducer.
// Unprojected frames still reach the reducer for welcome and decode diagnostics.
func (m *shellModel) projectFrame(frame state.Frame) (*streamProjection, tea.Cmd, bool) {
	var decoder protocol.ClientDecoder
	envelope, err := decoder.Decode(frame.Data)
	if err == nil && envelope != nil {
		if m.cardReply(*envelope) {
			return nil, nil, true
		}
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
		projection, err := projectStream(*envelope, m.styles.options.Language)
		if err != nil {
			m.status = ConnectionStatus{State: Failed, Cause: protocol.ErrorMalformed}
			return nil, nil, true
		}
		if projection.topic != "" {
			if (strings.HasPrefix(projection.topic, "conversation:") && projection.topic != "conversation:"+m.conversation.id || strings.HasPrefix(projection.topic, "results:") && projection.topic != "results:"+m.conversation.id) && m.conversation.id != "" {
				return nil, nil, true
			}
			if projection.resync {
				return nil, m.markResync(projection), true
			}
			if floor, pending := m.resyncPending[projection.topic]; pending && (!projection.snapshot || compareStreamCursor(projection.cursor, floor) < 0) {
				return nil, nil, true
			}
			if snapshot, ok := projection.message.(state.Snapshot); ok {
				snapshot.ClientTime = m.now()
				projection.message = snapshot
			}
			return &projection, nil, false
		}
	}
	return nil, nil, false
}

func (m *shellModel) applyData(message state.Msg) tea.Cmd {
	var projected *streamProjection
	if frame, ok := message.(state.Frame); ok {
		var command tea.Cmd
		var handled bool
		projected, command, handled = m.projectFrame(frame)
		if handled {
			return command
		}
		if projected != nil {
			message = projected.message
		}
	}
	switch projectedMessage := message.(type) {
	case state.Snapshot:
		projectedMessage.Notify = !m.focused
		message = projectedMessage
	case state.ItemDone:
		projectedMessage.Notify = !m.focused
		message = projectedMessage
	}
	beforeCard, _ := m.content.CurrentCard()
	var commands []state.Cmd
	m.content, commands = state.Reduce(m.content, message)
	for _, block := range m.content.Blocks {
		if block.Topic == "core" && block.ShellFileToolsAvailable != nil && !*block.ShellFileToolsAvailable {
			m.content, _ = state.Reduce(m.content, state.Notice{ID: "sandbox.unavailable", Code: "sandbox.unavailable"})
		}
	}
	m.refreshAfterData(message)
	afterCard, _ := m.content.CurrentCard()
	if beforeCard.ID != afterCard.ID || beforeCard.Revision != afterCard.Revision {
		m.cardGate.CardChanged(m.now())
		m.resetCardReading()
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
		if !m.conversation.agentPending && len(m.resyncPending) == 0 && !m.hasGap() && m.status.State == Resyncing {
			m.status = ConnectionStatus{State: Live}
		}
	}
	if m.hasGap() {
		m.status = ConnectionStatus{State: Resyncing}
	}
	var effects []tea.Cmd
	if projected != nil && (projected.topic == "progress" || (projected.topic == "waiting" || projected.topic == "running") && m.conversation.pager.board != nil) && m.conversation.pager.open && (m.conversation.pager.waiting || m.conversation.pager.board != nil) {
		effects = append(effects, m.waitingRequest())
	}
	effects = append(effects, m.effectCmds(commands)...)
	return tea.Batch(effects...)
}

func (m *shellModel) refreshAfterData(message state.Msg) {
	m.refreshBoard()
	m.refreshRunning()
	m.refreshProgress()
	m.refreshBlocks()
	m.refreshResultStreams(message)
	m.refreshOpenCard()
}

func (m *shellModel) effectCmds(commands []state.Cmd) []tea.Cmd {
	var effects []tea.Cmd
	for _, command := range commands {
		generation := m.generation
		effects = append(effects, func() tea.Msg {
			return shellEffect{Generation: generation, Message: command(context.Background())}
		})
	}
	return effects
}
