// SPDX-License-Identifier: Apache-2.0

package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestManagementNeverFallsThroughToConversation(t *testing.T) {
	for _, text := range []string{"/memory", "/agents", "/memory unknown", "/agents rename"} {
		t.Run(text, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: text})
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil || len(m.content.Blocks) != 0 || !m.conversation.pager.open || m.content.Draft.Text != "" {
				t.Fatal("unsupported management command entered conversation")
			}
		})
	}
}
func TestManagementUsesOpaqueTargetsAndExplicitScope(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "memory", Version: "1.0"}, protocol.Capability{Name: "agents", Version: "1.0"})
	m.conversation.id = "main:agent-a"
	_, cmd := m.managementCommand("/memory remember --scope=never-leaves --holder=agent synthetic statement")
	if cmd == nil {
		t.Fatal("no memory command")
	}
	p := cmd().(shellManagement).Request.(protocol.MemoryRequest)
	if *p.AgentID != "agent-a" || *p.Scope != "never-leaves" || *p.Holder != "agent" || *p.Text != "synthetic statement" {
		t.Fatal("scope or content changed")
	}
	_, cmd = m.managementCommand("/agents rename opaque-agent New display name")
	a := cmd().(shellManagement).Request.(protocol.AgentRequest)
	if *a.AgentID != "opaque-agent" || *a.Name != "New display name" {
		t.Fatal("rename used display name as identity")
	}
}
