// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestWebKeyIsHiddenAndNeverBecomesConversation(t *testing.T) {
	m := accountShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "web", Version: "1.0"})
	m.webCommand("/web")
	m.web.busy = false
	m.content.Draft.Text = "retained draft"
	m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	key := "fc-synthetic-private-key"
	m.Update(tea.PasteMsg{Content: key})
	if strings.Contains(m.View().Content, key) || m.content.Draft.Text != "retained draft" || len(m.conversation.undo) != 0 {
		t.Fatal("web key leaked into view or chat state")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("no key submission")
	}
	request := command().(shellWebRequest)
	if request.Request.Operation != "set" || request.Request.Key == nil || *request.Request.Key != key || m.web.input != "" {
		t.Fatal("key changed or retained")
	}
	m.Update(shellBegin{Generation: 2})
	if m.web.open || m.web.input != "" {
		t.Fatal("reconnect retained secret form")
	}
}
func TestUnsupportedWebSetupIsNotChat(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/web"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || len(m.content.Blocks) != 0 || m.web.code != "capability_not_negotiated" {
		t.Fatal("settings command became chat")
	}
}
