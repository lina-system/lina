// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type shellCardAnswer struct {
	Generation uint64
	Request    state.CardAnswerRequest
}

func (m *shellModel) cardCommand(msg state.Msg) tea.Cmd {
	var cmds []state.Cmd
	m.content, cmds = state.Reduce(m.content, msg)
	if len(cmds) == 0 {
		return nil
	}
	generation := m.generation
	return func() tea.Msg {
		request, ok := cmds[0](context.Background()).(state.CardAnswerRequest)
		if !ok {
			return nil
		}
		return shellCardAnswer{Generation: generation, Request: request}
	}
}

// The overlay receives keys before the conversation/composer. Paste is never a
// key and cannot reach this decision path. Selection and confirm are distinct.
func (m *shellModel) cardKey(key term.Key) (tea.Cmd, bool) {
	c, open := m.content.CurrentCard()
	if !open {
		if key.Name != "f6" || !m.cardGate.Admit(key, term.Toggle, m.now()) {
			return nil, false
		}
		for _, card := range m.content.Cards {
			if card.Outcome == "" {
				m.content, _ = state.Reduce(m.content, state.OpenCard{ID: card.ID})
				m.cardGate.CardChanged(m.now())
				m.cardField = 0
				m.cardScroll = 0
				return nil, true
			}
		}
		return nil, true
	}
	class := term.Approve
	if key.Name == "esc" || key.Name == "tab" || key.Name == "shift+tab" || key.Name == "up" || key.Name == "down" || key.Name == "pgup" || key.Name == "pgdown" {
		class = term.Toggle
	}
	if c.Kind == protocol.KindQuestion && key.Name != "ctrl+x" && key.Name != "enter" && key.Name != "ctrl+d" && key.Name != "esc" && key.Name != "tab" && key.Name != "shift+tab" {
		class = term.Edit
	}
	if !m.cardGate.Admit(key, class, m.now()) {
		return nil, true
	}
	if key.Name == "esc" {
		return m.cardCommand(state.CloseCard{}), true
	}
	switch key.Name {
	case "up":
		m.cardScroll = max(0, m.cardScroll-1)
		return nil, true
	case "down":
		rows, _ := m.cardRows(c)
		m.cardScroll = min(max(0, len(rows)-m.size.Height), m.cardScroll+1)
		return nil, true
	case "pgup":
		m.cardScroll = max(0, m.cardScroll-max(1, m.size.Height-1))
		return nil, true
	case "pgdown":
		rows, _ := m.cardRows(c)
		m.cardScroll = min(max(0, len(rows)-m.size.Height), m.cardScroll+max(1, m.size.Height-1))
		return nil, true
	}
	if !m.mutationsAllowed() || c.Outcome != "" || m.content.CardView.Pending {
		return nil, true
	}
	if c.Kind == protocol.KindApprovalRequest {
		switch key.Name {
		case "1", "2", "3":
			n, _ := strconv.Atoi(key.Name)
			if n <= len(c.Choices) {
				return m.cardCommand(state.ChooseApproval{Choice: c.Choices[n-1]}), true
			}
		case "enter":
			_, complete := m.cardRows(c)
			m.content, _ = state.Reduce(m.content, state.CardTargetsVisible{Complete: complete})
			return m.cardCommand(state.ConfirmCard{}), true
		}
		return nil, true
	}
	if key.Name == "ctrl+x" {
		return m.cardCommand(state.AnswerQuestion{Action: protocol.QuestionAnswerActionCancel}), true
	}
	if key.Name == "ctrl+d" {
		return m.cardCommand(state.AnswerQuestion{Action: protocol.QuestionAnswerActionDecline}), true
	}
	if c.Form == nil || len(c.Form.Fields) == 0 {
		return nil, true
	}
	m.cardField = min(m.cardField, len(c.Form.Fields)-1)
	switch key.Name {
	case "tab":
		m.cardField = (m.cardField + 1) % len(c.Form.Fields)
	case "shift+tab":
		m.cardField = (m.cardField + len(c.Form.Fields) - 1) % len(c.Form.Fields)
	case "enter":
		return m.cardCommand(state.AnswerQuestion{Action: protocol.QuestionAnswerActionAccept}), true
	default:
		field := c.Form.Fields[m.cardField]
		value := m.content.CardView.Fields[field.Name]
		if key.Name == "backspace" {
			if value != "" {
				_, size := utf8.DecodeLastRuneInString(value)
				value = value[:len(value)-size]
			}
		} else if utf8.RuneCountInString(key.Name) == 1 {
			value += key.Name
		} else if key.Name == "space" {
			value += " "
		} else {
			return nil, true
		}
		return m.cardCommand(state.EditQuestionField{Name: field.Name, Value: value}), true
	}
	return nil, true
}

// All target text wraps; it is never ellipsized. Confirm remains disabled when
// the full target area cannot fit, including after a resize or group update.
func (m *shellModel) cardRows(c state.Card) ([]string, bool) {
	width := max(1, m.size.Width)
	var rows []string
	add := func(text string) {
		rows = append(rows, strings.Split(ansi.Hardwrap(sanitize.Display(text), width, true), "\n")...)
	}
	add(c.Agent)
	if c.Recommendation != "" {
		add(c.Recommendation)
	}
	add(c.Summary)
	for _, field := range c.Targets {
		add(field.Name + ": " + field.Value)
	}
	complete := len(rows) <= m.size.Height-4 && m.size.Width >= 40 && m.size.Height >= 10
	if c.Form != nil {
		add(c.Form.PluginName)
		for i, field := range c.Form.Fields {
			prefix := "  "
			if i == m.cardField {
				prefix = "> "
			}
			add(prefix + field.Name + " (" + string(field.Type) + "): " + m.content.CardView.Fields[field.Name])
			if field.Choices != nil {
				add(strings.Join(*field.Choices, " / "))
			}
		}
	}
	if c.Outcome != "" {
		code := string(c.Outcome)
		if c.Outcome == state.CardStopped {
			code = "card.stopped"
		}
		add(design.Text(m.styles.options.Language, code))
	}
	if c.Kind == protocol.KindApprovalRequest {
		var choices []string
		for i, choice := range c.Choices {
			label := strconv.Itoa(i+1) + " " + design.Text(m.styles.options.Language, string(choice))
			if choice == m.content.CardView.Choice {
				label = "[" + label + "]"
			}
			choices = append(choices, label)
		}
		add(strings.Join(choices, "  "))
		id := "card.confirm"
		if !complete {
			id = "card.targets_cut"
		}
		add(design.Text(m.styles.options.Language, id))
	} else {
		add(design.Text(m.styles.options.Language, "card.question_keys"))
	}
	add(design.Text(m.styles.options.Language, "card.escape"))
	return rows, complete
}

func (m *shellModel) cardContent(c state.Card) string {
	if m.size.Height <= 0 || m.size.Width <= 0 {
		return ""
	}
	rows, _ := m.cardRows(c)
	height := max(0, m.size.Height-1)
	start := min(max(0, m.cardScroll), max(0, len(rows)-height))
	rows = rows[start:]
	if len(rows) > height {
		rows = rows[:height]
		if height > 0 {
			rows[height-1] = design.Text(m.styles.options.Language, "card.targets_cut")
		}
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	status := m.styles.presentation(m.status)
	rows = append(rows, status.Mark+" "+status.Label)
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], m.size.Width, "")
	}

	return strings.Join(rows, "\n")
}

func (m *shellModel) cardNotificationText(bell state.CardBell) string {
	if m.focused {
		return ""
	}
	switch m.styles.options.Notification {
	case "bell":
		return "\a"
	case "terminal":
		text := strings.NewReplacer("\n", " ", ";", ",").Replace(sanitize.Display(bell.Agent + ": " + bell.Summary))
		return "\x1b]9;" + ansi.Truncate(text, 160, "") + "\a"
	}
	return ""
}
func (m *shellModel) cardNotification(bell state.CardBell) tea.Cmd {
	if text := m.cardNotificationText(bell); text != "" {
		return tea.Raw(text)
	}
	return nil
}
func (d *display) notifyCard(m *shellModel, bell state.CardBell) error {
	text := m.cardNotificationText(bell)
	if text == "" || !d.interactive {
		return nil
	}
	if d.renderer != nil {
		return d.renderer.raw(text)
	}
	return d.write(text)
}

// cardReply consumes only a correlated result for a request this client sent.
// Diagnostic strings are never interpreted as a decision outcome.
func (m *shellModel) cardReply(envelope protocol.Envelope) bool {
	if envelope.ReplyTo == nil {
		return false
	}
	request, ok := m.cardRequests[*envelope.ReplyTo]
	if !ok {
		return false
	}
	delete(m.cardRequests, *envelope.ReplyTo)
	var outcome state.CardOutcome
	switch p := envelope.Payload.(type) {
	case protocol.ApprovalAnswer:
		if envelope.Kind != protocol.KindApprovalAnswer || request.QuestionAction != "" || p.CardID == nil || *p.CardID != request.ID || p.Revision == nil || *p.Revision != request.Token || p.Choice != request.Choice {
			break
		}
		if p.Outcome != nil {
			outcome = state.CardOutcome(*p.Outcome)
		}
	case protocol.QuestionAnswer:
		if envelope.Kind != protocol.KindQuestionAnswer || request.QuestionAction == "" || p.CardID == nil || *p.CardID != request.ID || p.Revision == nil || *p.Revision != request.Token || p.Answer != request.QuestionAction {
			break
		}
		if p.Outcome != nil {
			outcome = state.CardOutcome(*p.Outcome)
		}
	}
	if envelope.Error != nil {
		switch envelope.Error.Code {
		case protocol.ErrorStaleEpoch:
			outcome = state.CardStaleEpoch
		case protocol.ErrorStaleRevision:
			outcome = state.CardStaleGroup
		case protocol.ErrorUnsupportedCombination, protocol.ErrorCapabilityNotNegotiated, protocol.ErrorMalformed, protocol.ErrorNotAuthorized, protocol.ErrorOutOfScope, protocol.ErrorDuplicate, protocol.ErrorUnavailable:
			outcome = state.CardRefused
		}
	}
	if outcome == "" {
		m.status = ConnectionStatus{State: Failed, Cause: protocol.ErrorMalformed}
		return true
	}
	m.content, _ = state.Reduce(m.content, state.CardAnswerResult{Request: request, Outcome: outcome})
	m.refreshBlocks()
	return true
}

type cardPeer interface {
	AnswerApproval(context.Context, protocol.ApprovalAnswer) (string, error)
	AnswerQuestion(context.Context, protocol.QuestionAnswer) (string, error)
}

func sendCard(ctx context.Context, peer cardPeer, request state.CardAnswerRequest) (string, error) {
	if request.QuestionAction != "" {
		answer := protocol.QuestionAnswer{CardID: &request.ID, Revision: &request.Token, Answer: request.QuestionAction}
		if request.QuestionAction == protocol.QuestionAnswerActionAccept {
			answer.Values = &request.Values
		}
		return peer.AnswerQuestion(ctx, answer)
	}
	return peer.AnswerApproval(ctx, protocol.ApprovalAnswer{CardID: &request.ID, Revision: &request.Token, Choice: request.Choice})
}

func (m *shellModel) cardSummary(card state.Card) string {
	text := card.Agent
	if card.Recommendation != "" {
		text += " " + card.Recommendation
	}
	text += " [" + design.Text(m.styles.options.Language, string(card.Kind)) + "] " + card.Summary
	if card.Outcome != "" {
		code := string(card.Outcome)
		if card.Outcome == state.CardStopped {
			code = "card.stopped"
		}
		text += " (" + design.Text(m.styles.options.Language, code) + ")"
	}
	return sanitize.Display(text)
}
func (m *shellModel) waitingLine() string {
	count := 0
	summary := ""
	for _, card := range m.content.Cards {
		if card.Outcome == "" {
			count++
			if summary == "" {
				summary = card.Agent + ": " + card.Summary
			}
		}
	}
	if count == 0 {
		supported := false
		for _, cap := range m.content.Capabilities {
			supported = supported || cap.Name == "waiting"
		}
		if !supported {
			return design.Text(m.styles.options.Language, "shell.cards")
		}
	}
	text := design.Text(m.styles.options.Language, "card.open") + " (" + strconv.Itoa(count) + ")"
	if summary != "" {
		text += "  " + sanitize.Display(summary)
	}
	return text
}

func (m *shellModel) cardPaste(text string) tea.Cmd {
	card, ok := m.content.CurrentCard()
	if !ok || !m.mutationsAllowed() || card.Kind != protocol.KindQuestion || card.Outcome != "" || m.content.CardView.Pending || card.Form == nil || m.cardField < 0 || m.cardField >= len(card.Form.Fields) {
		return nil
	}
	field := card.Form.Fields[m.cardField]
	return m.cardCommand(state.EditQuestionField{Name: field.Name, Value: m.content.CardView.Fields[field.Name] + text})
}
