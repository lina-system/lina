// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"strings"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

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
