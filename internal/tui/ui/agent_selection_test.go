// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestReconnectHistoryRetainsChosenAgentAndUnacknowledgedDraft(t *testing.T) {
	m := conversationShell()
	m.conversation.id = "main:chosen"
	m.Update(tea.PasteMsg{Content: "pending"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.PasteMsg{Content: "new draft"})
	m.Update(shellBegin{Generation: 2})
	request := m.reconnectHistory([]protocol.Agent{{AgentID: "default"}, {AgentID: "chosen"}})
	if request == nil || request.AgentID == nil || *request.AgentID != "chosen" || m.activeAgent() != "chosen" {
		t.Fatal("reconnect changed the chosen destination")
	}
	if m.content.Draft.Text != "pending\nnew draft" || m.content.Busy {
		t.Fatal("reconnect lost or replayed pending input")
	}
	m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || m.content.Draft.Text != "pending\nnew draft" {
		t.Fatal("draft sent before history discovery completed")
	}
	m.agentDiscoveryDone() // The matching history reply admits input.
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil {
		t.Fatal("restored draft cannot be sent explicitly")
	}
}

func TestReconnectMissingAgentKeepsDraftUntilExplicitChoice(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			m := conversationShell()
			m.styles.options.Language = language
			m.conversation.id = "main:removed"
			m.content.Topics = map[string]state.Topic{"conversation:main:removed": {OutOfSync: true}}
			m.resyncPending["conversation:main:removed"] = protocol.StreamCursor{Epoch: "1", Seq: "1"}
			m.Update(tea.PasteMsg{Content: "preserved"})
			m.Update(shellBegin{Generation: 2})
			remaining := []protocol.Agent{{AgentID: "other"}}
			if request := m.reconnectHistory(remaining); request != nil {
				t.Fatal("missing agent triggered a fallback history request")
			}
			m.content.Agents = remaining
			m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
			if m.hasGap() || len(m.resyncPending) != 0 || m.localNotice() != design.Text(language, "agents.choose_required") {
				t.Fatal("removed subscriptions block explicit choice or notice is missing")
			}
			if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || m.content.Draft.Text != "preserved" || m.content.Busy {
				t.Fatal("missing selection sent or lost the draft")
			}
			if m.activeAgent() != "removed" || m.reconnectHistory(remaining) != nil {
				t.Fatal("another reconnect silently changed focus")
			}
			// Management remains available; even a fresh list or deletion of an
			// unrelated agent cannot implicitly authorize the draft destination.
			op, other, reply := "delete", "unrelated", "reply"
			m.content.Capabilities = []protocol.Capability{{Name: "agents", Version: "1.0"}}
			session := managementSession{model: m, pending: map[string]protocol.Payload{reply: protocol.AgentRequest{Operation: &op, AgentID: &other}}}
			_, err := session.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.AgentResult{Operation: &op, AgentID: &other, Agents: &remaining}})
			if err != nil || !m.conversation.agentMissing || m.activeAgent() != "removed" {
				t.Fatal("unrelated delete changed missing focus", err)
			}
			op, other = "choose", "other"
			session.pending[reply] = protocol.AgentRequest{Operation: &op, AgentID: &other}
			_, err = session.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.AgentResult{Operation: &op, AgentID: &other, Agents: &remaining}})
			if err != nil || m.conversation.agentMissing || m.activeAgent() != "other" || m.content.Draft.Text != "preserved" {
				t.Fatal("explicit choice did not restore focus while retaining the draft", err)
			}
		})
	}
}
