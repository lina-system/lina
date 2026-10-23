// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type textPager struct {
	result                   *resultPager
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

func (m *shellModel) openPager(text string) {
	m.conversation.pager = textPager{open: true, text: sanitize.Display(text)}
}

func (m *shellModel) pagerText() string {
	if !m.conversation.pager.transcript {
		return m.conversation.pager.text
	}
	// Read the current projection, without retaining another history copy that
	// could outlive authoritative replacements, revocation or a focus change.
	var messages []string
	for _, b := range m.content.Blocks {
		if !m.conversationBlock(b) {
			continue
		}
		text := b.Text
		if b.Card != nil {
			// Use the same current card summary as the conversation viewport.
			text = m.blocks[b.ID].text
		}
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
			if p.result != nil {
				if card.ResultID != p.result.request.ResultID || card.ResultRevision != string(*p.result.request.Revision) {
					*p = textPager{}
				}
				return
			}
			p.text = sanitize.Display(resultText(card))
			if p.followEnd {
				p.offset = len(m.pagerLines())
			}
			return
		}
	}
	if p.result != nil {
		*p = textPager{}
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
	if stream == nil || m.conversation.pager.result != nil {
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
