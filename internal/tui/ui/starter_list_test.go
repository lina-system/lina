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
