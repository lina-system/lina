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

type talkView struct {
	open, busy       bool
	selected, offset int
	attempt          uint64
	code, expanded   string
	next             *string
	rows             []protocol.AgentTalkLine
	events           []protocol.AgentTalkEvent
}
type shellTalkRequest struct {
	Generation, Attempt uint64
	ExchangeID          string
	Cursor              *string
}

func (m *shellModel) talkCommand(text string) (bool, tea.Cmd) {
	if text != "/talk" {
		return false, nil
	}
	attempt := m.talk.attempt + 1
	m.talk = talkView{open: true, attempt: attempt}
	if !m.hasCapability("exchanges") {
		m.talk.code = "capability_not_negotiated"
		return true, nil
	}
	return true, m.talkRequest("")
}
func (m *shellModel) talkRequest(id string) tea.Cmd { return m.talkPageRequest(id, nil) }
func (m *shellModel) talkPageRequest(id string, cursor *string) tea.Cmd {
	m.talk.busy = true
	m.talk.code = ""
	generation, attempt := m.generation, m.talk.attempt
	return func() tea.Msg { return shellTalkRequest{generation, attempt, id, cursor} }
}
func (m *shellModel) talkKey(msg tea.KeyMsg) tea.Cmd {
	key := term.FromTea(msg, m.events)
	if !m.gate.Admit(key, term.Toggle, m.now()) {
		return nil
	}
	v := &m.talk
	switch key.Name {
	case "ctrl+d":
		m.status.State = Quit
		return tea.Quit
	case "esc", "ctrl+c":
		if v.expanded != "" {
			v.attempt++
			v.busy = false
			v.next = nil
			v.expanded = ""
			v.events = nil
			v.offset = 0
			return nil
		}
		attempt := v.attempt + 1
		*v = talkView{attempt: attempt}
		return nil
	case "pgup":
		v.offset = max(0, v.offset-max(1, m.size.Height-2))
		return nil
	case "pgdown":
		v.offset += max(1, m.size.Height-2)
		return nil
	}
	if v.busy || m.status.State != Live {
		return nil
	}
	switch key.Name {
	case "up":
		v.selected = max(0, v.selected-1)
	case "down":
		v.selected = min(max(0, len(v.rows)-1), v.selected+1)
	case "r":
		return m.talkRequest("")
	case "enter":
		if v.expanded != "" {
			if v.next != nil {
				return m.talkPageRequest(v.expanded, v.next)
			}
			return nil
		}
		if len(v.rows) > 0 {
			return m.talkRequest(v.rows[v.selected].ExchangeID)
		}
	}
	return nil
}
func (m *shellModel) talkResponse(p protocol.AgentTalk) {
	v := &m.talk
	v.busy = false
	if p.Entries != nil {
		selected := ""
		if len(v.rows) > v.selected {
			selected = v.rows[v.selected].ExchangeID
		}
		// A repeated projection stays one row per exchange, keyed by Core identity.
		rows := make([]protocol.AgentTalkLine, 0, len(*p.Entries))
		indices := map[string]int{}
		for _, row := range *p.Entries {
			if i, ok := indices[row.ExchangeID]; ok {
				rows[i] = row
			} else {
				indices[row.ExchangeID] = len(rows)
				rows = append(rows, row)
			}
		}
		v.rows = rows
		v.selected = min(v.selected, max(0, len(rows)-1))
		if i, ok := indices[selected]; ok {
			v.selected = i
		}
		v.expanded = ""
		v.events = nil
	}
	if p.ExchangeID != nil && p.Events != nil {
		v.expanded = *p.ExchangeID
		v.events = *p.Events
		v.next = p.NextCursor
		v.offset = 0
	}
}
func (m *shellModel) talkContent() string {
	v := &m.talk
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	agent := func(id string) string {
		for _, a := range m.content.Agents {
			if a.AgentID == id {
				return sanitize.Display(a.Name)
			}
		}
		return text("agent")
	}
	// Summary content is one physical line; peer newlines cannot grow a row.
	oneLine := func(s string) string { return strings.Join(strings.Fields(sanitize.Display(s)), " ") }
	lines := []string{text("talk.heading")}
	if v.expanded != "" {
		for _, e := range v.events {
			lines = append(lines, agent(e.AgentID)+"  "+sanitize.Display(e.At), sanitize.Display(e.Text))
		}
	} else {
		for i, row := range v.rows {
			mark := "  "
			if i == v.selected {
				mark = "> "
			}
			line := mark + oneLine(agent(row.FromAgent)) + " / " + oneLine(agent(row.ToAgent)) + "  " + oneLine(row.Subject) + "  " + text(row.State) + " / " + text(row.Outcome)
			if row.NeedsPerson {
				line += "  " + text("talk.needs_person")
			}
			lines = append(lines, line)
		}
		if len(v.rows) == 0 {
			lines = append(lines, text("talk.empty"))
		}
	}
	if v.expanded != "" && v.next != nil {
		lines = append(lines, text("talk.more"))
	}
	if v.code != "" {
		lines = append(lines, text(v.code))
	}
	rows := strings.Split(strings.Join(lines, "\n"), "\n")
	available := max(0, m.size.Height-2)
	if v.expanded == "" {
		v.offset = max(v.offset, v.selected-available+2)
	}
	v.offset = min(v.offset, max(0, len(rows)-available))
	rows = rows[v.offset:min(len(rows), v.offset+available)]
	return strings.Join(append(rows, text("talk.help")), "\n")
}
