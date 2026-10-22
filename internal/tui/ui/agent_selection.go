// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (m *shellModel) discoveryHistory() *protocol.HistoryGet {
	if !m.hasCapability("conversation") {
		return nil
	}
	if m.conversation.agentMissing {
		m.agentDiscoveryDone()
		return nil
	}
	if m.conversation.id != "" {
		m.conversation.agentPending = true
		agent := m.activeAgent()
		return &protocol.HistoryGet{AgentID: &agent}
	}
	if len(m.content.Agents) == 0 {
		return nil
	}
	// Welcome precedes the History reply that supplies the destination.
	// Keep the visible connection recovering until that discovery completes.
	m.conversation.agentPending = true
	return &protocol.HistoryGet{}
}

// An unavailable read says nothing about membership. Reopen the connection to
// retry only discovery, leaving application input in the draft. Core uses
// out_of_scope when the selected agent is no longer available to this client.
func (m *shellModel) discoveryFailed(code protocol.ErrorCode) bool {
	if code == protocol.ErrorUnavailable {
		m.status = ConnectionStatus{State: Recovering, Cause: code}
		return true
	}
	if code == protocol.ErrorOutOfScope {
		m.reconnectHistory(nil)
	} else {
		m.status = connectionFailure(&clientapi.Refusal{Cause: protocol.Error{Code: code}})
	}
	return false
}

// A reconnect restores client focus by identity. An absent selection stays
// blocked until the person explicitly chooses an agent, even across retries.
// Validate against a fresh agent list: welcome may advertise only the default.
func (m *shellModel) reconnectHistory(agents []protocol.Agent) *protocol.HistoryGet {
	if m.conversation.id == "" && !m.conversation.agentMissing {
		if len(agents) == 0 {
			return nil
		}
		return &protocol.HistoryGet{}
	}
	agent := strings.TrimPrefix(m.conversation.id, "main:")
	if !m.conversation.agentMissing {
		for _, a := range agents {
			if a.AgentID == agent {
				return &protocol.HistoryGet{AgentID: &agent}
			}
		}
	}
	m.conversation.agentMissing = true
	m.reduceInput(state.FocusConversation{})
	for topic := range m.resyncPending {
		if strings.HasPrefix(topic, "conversation:") || strings.HasPrefix(topic, "results:") {
			delete(m.resyncPending, topic)
		}
	}
	m.agentDiscoveryDone()
	return nil
}

func (m *shellModel) agentDiscoveryDone() {
	m.conversation.agentPending = false
	if m.status.State == Resyncing && !m.hasGap() && len(m.resyncPending) == 0 {
		m.status = ConnectionStatus{State: Live}
	}
}

func switchConversation(ctx context.Context, c *clientapi.Client, m *shellModel, agent string) error {
	if m.conversation.id != "" && !m.conversation.agentMissing {
		off := false
		for _, topic := range []string{"conversation:" + m.conversation.id, "results:" + m.conversation.id} {
			if _, err := c.Subscribe(ctx, protocol.Subscribe{Topic: &topic, Active: &off}); err != nil {
				return err
			}
		}
	}
	m.reduceInput(state.FocusConversation{})
	m.conversation.id = ""
	m.conversation.agentMissing = false
	m.agentDiscoveryDone()
	m.conversation.scroll = 0
	m.conversation.undo = nil
	if agent == "" {
		return nil
	}
	// Set the expected identity now, so late events from the old subscriptions
	// cannot be projected while discovery of the new lane is in flight.
	m.conversation.id = "main:" + agent
	_, err := c.History(ctx, protocol.HistoryGet{AgentID: &agent})
	return err
}
