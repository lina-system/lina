// SPDX-License-Identifier: Apache-2.0

package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestTalkListReplaysOnceAndExpandsSelectedIdentity(t *testing.T) {
	m := accountShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "exchanges", Version: "1.0"})
	m.talkCommand("/talk")
	rows := []protocol.AgentTalkLine{
		{ExchangeID: "one", Subject: "first", State: "read", Outcome: "pending"},
		{ExchangeID: "two", Subject: "second\n\x1b]52;c;ZXZpbA==\x07", State: "answered", Outcome: "checked"},
	}
	m.talkResponse(protocol.AgentTalk{Entries: &rows})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	replay := append(append([]protocol.AgentTalkLine{}, rows...), rows[0])
	m.talkResponse(protocol.AgentTalk{Entries: &replay})
	if len(m.talk.rows) != 2 || m.talk.rows[m.talk.selected].ExchangeID != "two" {
		t.Fatal("replay duplicated rows or changed selection")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	request := command().(shellTalkRequest)
	if request.ExchangeID != "two" {
		t.Fatal("expansion targeted a different exchange")
	}
	if strings.Contains(m.talkContent(), "\x1b") || strings.Contains(m.talkContent(), "ZXZpbA") {
		t.Fatal("peer terminal escape reached log")
	}
	events := []protocol.AgentTalkEvent{{Text: "detail text", AgentID: "agent", At: "synthetic time"}}
	m.talkResponse(protocol.AgentTalk{ExchangeID: &request.ExchangeID, Events: &events})
	if !strings.Contains(m.talkContent(), "detail text") {
		t.Fatal("expansion did not render ledger content")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.talk.open || m.talk.expanded != "" || m.talk.events != nil {
		t.Fatal("closing detail lost list or retained events")
	}
	m.Update(shellBegin{Generation: 2})
	if m.talk.open || m.talk.rows != nil {
		t.Fatal("reconnect retained prior authority's log")
	}
}
func TestUnsupportedTalkNeverBecomesChat(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/talk"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || len(m.content.Blocks) != 0 || m.talk.code != "capability_not_negotiated" {
		t.Fatal("unsupported talk command became model input")
	}
}

func TestTalkExpansionRequestsNextPageWithoutLosingExchange(t *testing.T) {
	m := accountShell()
	m.talk = talkView{open: true}
	events := []protocol.AgentTalkEvent{{Text: "first page"}}
	m.talkResponse(protocol.AgentTalk{ExchangeID: stringPointer("exchange"), Events: &events, NextCursor: stringPointer("0:32768")})
	cmd := m.talkKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("next page unavailable")
	}
	req := cmd().(shellTalkRequest)
	if req.ExchangeID != "exchange" || req.Cursor == nil || *req.Cursor != "0:32768" {
		t.Fatal("page lost its identity")
	}
	events = []protocol.AgentTalkEvent{{Text: "last page"}}
	m.talkResponse(protocol.AgentTalk{ExchangeID: stringPointer("exchange"), Events: &events})
	if m.talk.next != nil || len(m.talk.events) != 1 || m.talk.events[0].Text != "last page" {
		t.Fatal("page was duplicated or continuation retained")
	}
}
