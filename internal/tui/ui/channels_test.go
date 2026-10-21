// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestChannelTokenNeverEntersConversation(t *testing.T) {
	m := accountShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "channels", Version: "1.0"})
	m.channelCommand("/channels")
	m.channels.busy = false
	m.content.Draft.Text = "retained draft"
	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	token := "12345:SYNTHETIC-PRIVATE-TOKEN"
	m.Update(tea.PasteMsg{Content: token})
	if strings.Contains(m.View().Content, token) || m.content.Draft.Text != "retained draft" || len(m.conversation.undo) != 0 {
		t.Fatal("token leaked into view, draft or undo")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	request := command().(shellChannelRequest)
	if request.Request.Token == nil || *request.Request.Token != token || m.channels.input != "" {
		t.Fatal("token was changed or retained")
	}
	m.Update(shellBegin{Generation: 2})
	if m.channels.open || m.channels.input != "" || m.channels.pairing != "" {
		t.Fatal("reconnect retained channel form")
	}
}

func TestUnsupportedChannelCommandNeverBecomesChat(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/channels"})
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || len(m.content.Blocks) != 0 || m.channels.code != "capability_not_negotiated" {
		t.Fatal("unsupported channel command sent as model input")
	}
}

func TestChannelUnbindSelectsExactChatAndTopic(t *testing.T) {
	m := accountShell()
	m.channels = channelView{open: true, rows: []protocol.ChannelConnection{{Account: "bot", Bindings: []protocol.ChannelBinding{{Chat: "chat-one", Topic: ""}, {Chat: "chat-two", Topic: "thread"}}}}}
	m.Update(tea.KeyPressMsg{Code: 'u', Text: "u"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no unbind action for selected binding")
	}
	p := cmd().(shellChannelRequest).Request
	if p.Operation == nil || *p.Operation != "unbind" || p.Chat == nil || *p.Chat != "chat-two" || p.Topic == nil || *p.Topic != "thread" {
		t.Fatal("unbind targeted a different binding")
	}
}
