// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestTelegramSecretsStayOutOfConversation(t *testing.T) {
	m := accountShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "telegram_source", Version: "1.0"})
	m.telegramCommand("/telegram")
	m.telegram.busy = false
	m.telegram.stage = "password"
	m.content.Draft.Text = "retained draft"
	m.Update(tea.PasteMsg{Content: "synthetic-private-password"})
	if strings.Contains(m.View().Content, "synthetic-private-password") || m.content.Draft.Text != "retained draft" || len(m.conversation.undo) != 0 {
		t.Fatal("secret reached view or conversation")
	}
	t.Logf("screen capture:\n%s", ansi.Strip(m.View().Content))
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	request := cmd().(shellTelegramRequest)
	if request.Request.Answer == nil || *request.Request.Answer != "synthetic-private-password" || m.telegram.input != "" {
		t.Fatal("answer was lost or retained")
	}
	m.Update(shellBegin{Generation: 2})
	if m.telegram.open || m.telegram.input != "" {
		t.Fatal("reconnect retained sign-in")
	}
}
func TestUnsupportedTelegramCommandNeverBecomesChat(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/telegram"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || len(m.content.Blocks) != 0 || m.telegram.code != "capability_not_negotiated" {
		t.Fatal("settings became a model input")
	}
}
