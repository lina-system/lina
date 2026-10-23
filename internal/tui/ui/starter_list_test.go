// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestAgentListPresentsRoleSourceAndMainConversation(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			m := conversationShell()
			m.styles.options.Language = language
			list := []protocol.Agent{}
			for key, name := range map[string]string{"system": "Selene", "lina": "Lina", "dev": "Sion", "knowledge": "Rumi", "life": "Arin", "creative": "Cleo", "investing": "Kai", "legal": "Kanna"} {
				list = append(list, protocol.Agent{AgentID: "id-" + key, Name: name, Preset: &key})
			}
			op, reply := "list", "reply"
			s := managementSession{model: m, pending: map[string]protocol.Payload{reply: protocol.AgentRequest{Operation: &op}}}
			if _, err := s.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.AgentResult{Operation: &op, Agents: &list}}); err != nil {
				t.Fatal(err)
			}
			// Read the pager's entire content, independent of viewport clipping.
			for _, a := range list {
				role, ok := design.Lookup(language, "agents.role."+*a.Preset)
				if !ok {
					t.Fatalf("no role for %s", *a.Preset)
				}
				for _, want := range []string{a.Name, *a.Preset, role, "main:" + a.AgentID} {
					if !strings.Contains(m.conversation.pager.text, want) {
						t.Errorf("agent list omitted %q", want)
					}
				}
			}
		})
	}
}

// The list must show the value /agents choose takes, labelled apart from the
// conversation id that Core refuses as an agent id.
func TestAgentListShowsTheIdentifierChooseAccepts(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			m := conversationShell()
			m.styles.options.Language = language
			m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "agents", Version: "1.0"})
			preset := "life"
			list := []protocol.Agent{{AgentID: "agent-arin", Name: "Arin", Preset: &preset}}
			op, reply := "list", "reply"
			s := managementSession{model: m, pending: map[string]protocol.Payload{reply: protocol.AgentRequest{Operation: &op}}}
			if _, err := s.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.AgentResult{Operation: &op, Agents: &list}}); err != nil {
				t.Fatal(err)
			}
			shown := listedValue(t, m.conversation.pager.text, design.Text(language, "agents.id")+": ")
			if shown != "agent-arin" {
				t.Fatalf("listed id = %q, want the agent id", shown)
			}
			if !strings.Contains(m.conversation.pager.text, "  main:agent-arin") {
				t.Fatalf("conversation id missing: %s", m.conversation.pager.text)
			}
			for _, arg := range []string{shown, "main:agent-arin"} {
				handled, cmd := m.managementCommand("/agents choose " + arg)
				if !handled || cmd == nil {
					t.Fatalf("choose refused %q", arg)
				}
				request := cmd().(shellManagement).Request.(protocol.AgentRequest)
				if request.AgentID == nil || *request.AgentID != "agent-arin" {
					t.Fatalf("choose %q sent %v, want the agent id", arg, request.AgentID)
				}
			}
		})
	}
}

func listedValue(t *testing.T, text, label string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), label); ok {
			return value
		}
	}
	t.Fatalf("no %q line in %s", label, text)
	return ""
}
