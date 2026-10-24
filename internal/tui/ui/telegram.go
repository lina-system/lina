// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
)

type telegramView struct {
	open, busy                         bool
	stage, input, code, apiID, apiHash string
	rows                               []protocol.TelegramSource
	selected                           int
	attempt                            uint64
}
type shellTelegramRequest struct {
	Generation, Attempt uint64
	Request             protocol.TelegramRequest
}

func (m *shellModel) telegramCommand(text string) (bool, tea.Cmd) {
	if text != "/telegram" {
		return false, nil
	}
	m.telegram = telegramView{open: true, attempt: m.telegram.attempt + 1}
	if !m.hasCapability("telegram_source") {
		m.telegram.code = "capability_not_negotiated"
		return true, nil
	}
	return true, m.telegramRequest(protocol.TelegramRequest{Operation: "list"})
}
func (m *shellModel) telegramRequest(p protocol.TelegramRequest) tea.Cmd {
	m.telegram.busy = true
	m.telegram.code = ""
	generation, attempt := m.generation, m.telegram.attempt
	return func() tea.Msg { return shellTelegramRequest{generation, attempt, p} }
}
func (m *shellModel) telegramPaste(text string) {
	if !m.telegram.busy && m.telegram.stage != "" && len(text) <= 1024 {
		m.telegram.input = text
	}
}
func (m *shellModel) telegramKey(msg tea.KeyMsg) tea.Cmd {
	v := &m.telegram
	key := term.FromTea(msg, m.events)
	class := term.Toggle
	if v.stage != "" {
		class = term.Edit
	}
	if key.Name == "enter" || key.Name == "esc" || key.Name == "ctrl+c" || key.Name == "ctrl+d" {
		class = term.Toggle
	}
	if !m.gate.Admit(key, class, m.now()) {
		return nil
	}
	switch key.Name {
	case "ctrl+d":
		m.status.State = Quit
		return tea.Quit
	case "esc", "ctrl+c":
		cmd := m.telegramRequest(protocol.TelegramRequest{Operation: "cancel"})
		*v = telegramView{attempt: v.attempt}
		return cmd
	}
	if !m.mutationsAllowed() || v.busy || !m.hasCapability("telegram_source") {
		return nil
	}
	if v.stage != "" {
		switch key.Name {
		case "enter":
			input := v.input
			v.input = ""
			if input == "" {
				return nil
			}
			switch v.stage {
			case "api_id":
				v.apiID = strings.TrimSpace(input)
				v.stage = "api_hash"
			case "api_hash":
				v.apiHash = strings.TrimSpace(input)
				v.stage = "phone"
			case "phone":
				id, hash := v.apiID, v.apiHash
				v.apiID, v.apiHash = "", ""
				v.stage = ""
				return m.telegramRequest(protocol.TelegramRequest{Operation: "start", APIID: &id, APIHash: &hash, Phone: &input})
			case "code", "password":
				v.stage = ""
				return m.telegramRequest(protocol.TelegramRequest{Operation: "answer", Answer: &input})
			}
		case "backspace":
			runes := []rune(v.input)
			if len(runes) > 0 {
				v.input = string(runes[:len(runes)-1])
			}
		default:
			if len(v.input)+len(msg.Key().Text) <= 1024 {
				v.input += msg.Key().Text
			}
		}
		return nil
	}
	switch key.Name {
	case "c":
		v.stage = "api_id"
	case "r":
		return m.telegramRequest(protocol.TelegramRequest{Operation: "list"})
	case "up":
		v.selected = max(0, v.selected-1)
	case "down":
		v.selected = min(max(0, len(v.rows)-1), v.selected+1)
	case "x":
		if len(v.rows) > 0 {
			account := v.rows[v.selected].Account
			return m.telegramRequest(protocol.TelegramRequest{Operation: "disconnect", Account: &account})
		}
	}
	return nil
}
func (m *shellModel) telegramReply(attempt uint64, e protocol.Envelope) {
	v := &m.telegram
	if !v.open || v.attempt != attempt {
		return
	}
	v.busy = false
	if e.Error != nil {
		v.code = string(e.Error.Code)
		v.stage = ""
		v.input = ""
		return
	}
	if p, ok := e.Payload.(protocol.TelegramResult); ok {
		v.rows = p.Sources
		v.selected = min(v.selected, max(0, len(v.rows)-1))
		if p.Stage == "code" || p.Stage == "password" {
			v.stage = p.Stage
		} else {
			v.stage = ""
		}
	}
}
func (m *shellModel) telegramContent() string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	v := &m.telegram
	lines := []string{text("telegram.heading"), text("telegram.setup")}
	for i, row := range v.rows {
		mark := "  "
		if i == v.selected {
			mark = "> "
		}
		lines = append(lines, mark+sanitize.Display(row.Account)+"  "+text(row.State))
	}
	if v.code != "" {
		lines = append(lines, text(v.code))
	}
	if v.stage != "" {
		lines = append(lines, text("telegram."+v.stage), strings.Repeat("•", min(40, len([]rune(v.input)))))
	} else {
		lines = append(lines, text("telegram.help"))
	}
	return strings.Join(lines, "\n")
}
