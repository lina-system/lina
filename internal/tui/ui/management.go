// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

type shellManagement struct {
	Generation uint64
	Request    protocol.Payload
}

func (m *shellModel) activeAgent() string {
	if strings.HasPrefix(m.conversation.id, "main:") {
		return strings.TrimPrefix(m.conversation.id, "main:")
	}
	if len(m.content.Agents) > 0 {
		return m.content.Agents[0].AgentID
	}
	return ""
}
func (m *shellModel) managementHelp(kind string) string {
	if kind == "agents" {
		return "/agents\n/agents create <name>\n/agents choose <id>\n/agents rename <id> <name>\n/agents delete <id>"
	}
	return "/memory\n/memory list <query>\n/memory remember <text>\n/memory correct <id> <text>\n/memory forget <id>\nremember: --holder=all|agent --scope=local|private|never-leaves"
}
func cutCommand(s string) (string, string) {
	first, rest, _ := strings.Cut(strings.TrimSpace(s), " ")
	return first, strings.TrimSpace(rest)
}
func (m *shellModel) managementCommand(text string) (bool, tea.Cmd) {
	head, rest := cutCommand(text)
	if head != "/memory" && head != "/agents" {
		return false, nil
	}
	kind := strings.TrimPrefix(head, "/")
	help := m.managementHelp(kind)
	if !m.hasCapability(kind) {
		m.openPager(design.Text(m.styles.options.Language, "capability_not_negotiated") + "\n" + help)
		return true, nil
	}
	op, args := cutCommand(rest)
	if op == "" {
		op = "list"
	}
	var request protocol.Payload
	valid := true
	if kind == "agents" {
		p := protocol.AgentRequest{Operation: &op}
		switch op {
		case "list":
			valid = args == ""
		case "create":
			p.Name = &args
			valid = args != ""
		case "choose", "delete":
			p.AgentID = &args
			valid = args != "" && !strings.Contains(args, " ")
		case "rename":
			id, name := cutCommand(args)
			p.AgentID, p.Name = &id, &name
			valid = id != "" && name != ""
		default:
			valid = false
		}
		request = p
	} else {
		agent := m.activeAgent()
		p := protocol.MemoryRequest{Operation: &op, AgentID: &agent}
		switch op {
		case "list":
			if args != "" {
				p.Text = &args
			}
		case "remember":
			for strings.HasPrefix(args, "--holder=") || strings.HasPrefix(args, "--scope=") {
				flag, tail := cutCommand(args)
				args = tail
				key, value, ok := strings.Cut(flag, "=")
				if !ok || value == "" {
					valid = false
					break
				}
				switch key {
				case "--holder":
					if value != "all" && value != "agent" {
						valid = false
					}
					p.Holder = &value
				case "--scope":
					if value != "local" && value != "private" && value != "never-leaves" {
						valid = false
					}
					p.Scope = &value
				default:
					valid = false
				}
			}
			p.Text = &args
			valid = valid && args != ""
		case "correct":
			id, body := cutCommand(args)
			p.ItemID, p.Text = &id, &body
			valid = id != "" && body != ""
		case "forget":
			p.ItemID = &args
			valid = args != "" && !strings.Contains(args, " ")
		default:
			valid = false
		}
		valid = valid && agent != ""
		request = p
	}
	if !valid {
		m.openPager(design.Text(m.styles.options.Language, "malformed") + "\n" + help)
		return true, nil
	}
	m.openPager(help)
	generation := m.generation
	return true, func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}

type managementSession struct {
	model   *shellModel
	pending map[string]protocol.Payload
}

func (s *managementSession) request(ctx context.Context, c *clientapi.Client, p protocol.Payload) error {
	var id string
	var err error
	switch p := p.(type) {
	case protocol.AgentRequest:
		id, err = c.Agent(ctx, p)
	case protocol.MemoryRequest:
		id, err = c.Memory(ctx, p)
	}
	if err == nil {
		s.pending[id] = p
	}
	return err
}
func (s *managementSession) response(ctx context.Context, c *clientapi.Client, e protocol.Envelope) (bool, error) {
	if e.ReplyTo == nil {
		return false, nil
	}
	request, ok := s.pending[*e.ReplyTo]
	if !ok {
		return false, nil
	}
	delete(s.pending, *e.ReplyTo)
	m := s.model
	if e.Error != nil {
		m.openPager(design.Text(m.styles.options.Language, string(e.Error.Code)))
		return true, nil
	}
	switch p := e.Payload.(type) {
	case protocol.MemoryResult:
		if _, ok := request.(protocol.MemoryRequest); !ok || p.Operation == nil {
			return true, errStream
		}
		lines := []string{design.Text(m.styles.options.Language, *p.Operation), m.managementHelp("memory"), ""}
		if p.ItemID != nil {
			lines = append(lines, design.Text(m.styles.options.Language, *p.Operation)+" "+*p.ItemID)
		}
		if p.Items != nil {
			lines = append(lines, fmt.Sprintf("%d", len(*p.Items)))
			for _, item := range *p.Items {
				lines = append(lines, item.ItemID+"  "+item.Holder+"  "+design.Text(m.styles.options.Language, item.Scope), item.Content, strings.Join(item.Evidence, "  "))
			}
		}
		m.openPager(strings.Join(lines, "\n"))
	case protocol.AgentResult:
		original, ok := request.(protocol.AgentRequest)
		if !ok || p.Operation == nil || p.Agents == nil {
			return true, errStream
		}
		active := m.activeAgent()
		m.content.Agents = *p.Agents
		lines := []string{design.Text(m.styles.options.Language, *p.Operation), m.managementHelp("agents"), ""}
		if p.AgentID != nil {
			lines = append(lines, design.Text(m.styles.options.Language, *p.Operation)+" "+*p.AgentID)
		}
		found := false
		for _, a := range *p.Agents {
			lines = append(lines, a.AgentID+"  "+a.Name)
			found = found || a.AgentID == active
		}
		m.openPager(strings.Join(lines, "\n"))
		if *p.Operation == "choose" {
			if p.AgentID == nil || original.AgentID == nil || *p.AgentID != *original.AgentID {
				return true, errStream
			}
			active = *p.AgentID
		} else if active == "" && *p.Operation == "create" && p.AgentID != nil {
			active = *p.AgentID
		} else if !found && *p.Operation == "delete" {
			active = ""
			if len(*p.Agents) > 0 {
				active = (*p.Agents)[0].AgentID
			}
		} else {
			return true, nil
		}
		if !m.hasCapability("conversation") {
			m.conversation.id = "main:" + active
			return true, nil
		}
		if err := switchConversation(ctx, c, m, active); err != nil {
			return true, err
		}
	default:
		return true, errStream
	}
	return true, nil
}
