// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type channelView struct {
	open, busy                           bool
	stage, input, code, pairing, expires string
	selected, agent, bound, offset       int
	rows                                 []protocol.ChannelConnection
	binding                              protocol.ChannelRequest
	attempt                              uint64
}
type shellChannelRequest struct {
	Generation, Attempt uint64
	Request             protocol.ChannelRequest
}

func (m *shellModel) channelCommand(text string) (bool, tea.Cmd) {
	if text != "/channels" {
		return false, nil
	}
	attempt := m.channels.attempt + 1
	m.channels = channelView{open: true, attempt: attempt}
	if !m.hasCapability("channels") {
		m.channels.code = "capability_not_negotiated"
		return true, nil
	}
	return true, m.channelRequest(protocol.ChannelRequest{Operation: stringPointer("list")})
}
func (m *shellModel) channelRequest(p protocol.ChannelRequest) tea.Cmd {
	m.channels.busy = true
	m.channels.code = ""
	generation, attempt := m.generation, m.channels.attempt
	return func() tea.Msg { return shellChannelRequest{generation, attempt, p} }
}
func (m *shellModel) channelPaste(text string) {
	if !m.channels.busy && (m.channels.stage == "token" || m.channels.stage == "chat" || m.channels.stage == "topic") && len(text) <= 256 {
		m.channels.input = text
	}
}
func (m *shellModel) channelKey(msg tea.KeyMsg) tea.Cmd {
	v := &m.channels
	key := term.FromTea(msg, m.events)
	class := term.Toggle
	if v.stage == "token" || v.stage == "chat" || v.stage == "topic" {
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
		if v.stage != "" {
			v.stage, v.input = "", ""
			v.binding = protocol.ChannelRequest{}
			return nil
		}
		attempt := v.attempt + 1
		*v = channelView{attempt: attempt}
		return nil
	case "pgup":
		v.offset = max(0, v.offset-max(1, m.size.Height-2))
		return nil
	case "pgdown":
		v.offset += max(1, m.size.Height-2)
		return nil
	}
	if !m.mutationsAllowed() || v.busy {
		return nil
	}
	if v.stage == "token" || v.stage == "chat" || v.stage == "topic" {
		switch key.Name {
		case "enter":
			input := strings.TrimSpace(v.input)
			v.input = ""
			switch v.stage {
			case "token":
				if input == "" {
					return nil
				}
				v.stage = ""
				return m.channelRequest(protocol.ChannelRequest{Operation: stringPointer("connect"), Token: &input})
			case "chat":
				if input != "" {
					v.binding.Chat = &input
					v.binding.Scope.Memory = input == v.rows[v.selected].PrivateChat
				}
				v.stage = "topic"
			case "topic":
				v.binding.Topic = &input
				v.stage = "scope"
			}
		case "backspace":
			runes := []rune(v.input)
			if len(runes) > 0 {
				v.input = string(runes[:len(runes)-1])
			}
		default:
			if len(v.input)+len(msg.Key().Text) <= 256 {
				v.input += msg.Key().Text
			}
		}
		return nil
	}
	if v.stage == "unbind" {
		if len(v.rows) == 0 {
			return nil
		}
		bindings := v.rows[v.selected].Bindings
		switch key.Name {
		case "up":
			v.bound = max(0, v.bound-1)
		case "down":
			v.bound = min(max(0, len(bindings)-1), v.bound+1)
		case "enter":
			if len(bindings) == 0 {
				return nil
			}
			binding := bindings[v.bound]
			account := v.rows[v.selected].Account
			v.stage = ""
			return m.channelRequest(protocol.ChannelRequest{Operation: stringPointer("unbind"), Account: &account, Chat: &binding.Chat, Topic: &binding.Topic})
		}
		return nil
	}
	if v.stage == "scope" {
		s := v.binding.Scope
		switch key.Name {
		case "up":
			v.agent = max(0, v.agent-1)
		case "down":
			v.agent = min(max(0, len(m.content.Agents)-1), v.agent+1)
		case "r":
			switch s.Reads {
			case "dm_only":
				s.Reads = "addressed"
			case "addressed":
				s.Reads = "all_messages"
			default:
				s.Reads = "dm_only"
			}
		case "w":
			if s.Writes == "reply_only" {
				s.Writes = "may_start"
			} else {
				s.Writes = "reply_only"
			}
		case "t":
			s.Tools = !s.Tools
		case "m":
			s.Memory = !s.Memory
		case "enter":
			if len(m.content.Agents) == 0 {
				return nil
			}
			p := v.binding
			p.AgentID = stringPointer(m.content.Agents[v.agent].AgentID)
			v.stage = ""
			v.binding = protocol.ChannelRequest{}
			return m.channelRequest(p)
		}
		return nil
	}
	switch key.Name {
	case "up":
		v.pairing, v.expires = "", ""
		v.selected = max(0, v.selected-1)
	case "down":
		v.pairing, v.expires = "", ""
		v.selected = min(max(0, len(v.rows)-1), v.selected+1)
	case "c":
		v.stage, v.input = "token", ""
	case "r":
		return m.channelRequest(protocol.ChannelRequest{Operation: stringPointer("list")})
	case "u":
		if len(v.rows) > 0 && len(v.rows[v.selected].Bindings) > 0 {
			v.stage = "unbind"
			v.bound = 0
		}
	case "p", "b", "x":
		if len(v.rows) == 0 {
			return nil
		}
		row := v.rows[v.selected]
		p := protocol.ChannelRequest{Account: &row.Account}
		switch key.Name {
		case "p":
			p.Operation = stringPointer("pair")
		case "x":
			p.Operation = stringPointer("disconnect")
		case "b":
			v.binding = protocol.ChannelRequest{Operation: stringPointer("bind"), Account: &row.Account, Scope: &protocol.ChannelScope{Reads: "dm_only", Writes: "reply_only", Tools: true, Memory: true}}
			v.stage, v.input = "chat", ""
			return nil
		}
		return m.channelRequest(p)
	}
	return nil
}
func (m *shellModel) channelResponse(p protocol.ChannelResult) {
	v := &m.channels
	v.busy = false
	if p.Connections != nil {
		v.rows = *p.Connections
		v.selected = min(v.selected, max(0, len(v.rows)-1))
	}
	if p.PairingCode != nil {
		v.pairing = *p.PairingCode
	}
	if p.ExpiresAt != nil {
		v.expires = *p.ExpiresAt
	}
	if p.Operation != nil && (*p.Operation == "connect" || *p.Operation == "bind" || *p.Operation == "disconnect") {
		v.pairing, v.expires = "", ""
	}
}
func (m *shellModel) channelContent() string {
	v := &m.channels
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	lines := []string{text("channel.heading")}
	for i, row := range v.rows {
		mark := "  "
		if i == v.selected {
			mark = "> "
		}
		lines = append(lines, mark+sanitize.Display(row.Platform)+" "+sanitize.Display(row.Account)+"  "+text(row.Status))
		for j, b := range row.Bindings {
			agent := text("agent")
			for _, a := range m.content.Agents {
				if a.AgentID == b.AgentID {
					agent = sanitize.Display(a.Name)
				}
			}
			bindingMark := "  "
			if v.stage == "unbind" && i == v.selected && j == v.bound {
				bindingMark = "> "
			}
			lines = append(lines, bindingMark+sanitize.Display(b.Chat)+" "+sanitize.Display(b.Topic)+"  "+agent+"  "+m.channelScopeText(b.Scope))
		}
	}
	if len(v.rows) == 0 {
		lines = append(lines, text("channel.empty"))
	}
	if v.pairing != "" {
		lines = append(lines, text("channel.pair_help"), sanitize.Display(v.pairing), sanitize.Display(v.expires))
	}
	if v.code != "" {
		lines = append(lines, text(v.code))
	}
	if v.stage != "" {
		lines = append(lines, text("channel."+v.stage))
		input := sanitize.Display(v.input)
		if v.stage == "token" {
			input = strings.Repeat("*", min(40, len(v.input)))
		}
		if v.stage == "scope" {
			for i, a := range m.content.Agents {
				mark := "  "
				if i == v.agent {
					mark = "> "
				}
				lines = append(lines, mark+sanitize.Display(a.Name))
			}
			input = m.channelScopeText(*v.binding.Scope)
		}
		lines = append(lines, input)
	} else if !v.busy {
		lines = append(lines, text("channel.help"))
	}
	rows := strings.Split(strings.Join(lines, "\n"), "\n")
	available := max(0, m.size.Height-2)
	v.offset = min(v.offset, max(0, len(rows)-available))
	rows = rows[v.offset:min(len(rows), v.offset+available)]
	return strings.Join(append(rows, "Esc  Ctrl+D  PgUp/PgDown"), "\n")
}
func (m *shellModel) channelScopeText(s protocol.ChannelScope) string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	tools, memory := "channel.tools_off", "channel.memory_off"
	if s.Tools {
		tools = "channel.tools_on"
	}
	if s.Memory {
		memory = "channel.memory_on"
	}
	return strings.Join([]string{text(s.Reads), text(s.Writes), text(tools), text(memory)}, "; ")
}
