// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

type discoverySession struct {
	model                                  *shellModel
	agentListID, reconnectAgent, historyID string
}

func (s *discoverySession) reset() {
	s.agentListID, s.reconnectAgent, s.historyID = "", "", ""
}

func (s *discoverySession) start(ctx context.Context, c shellClient) error {
	m := s.model
	var err error
	// Initial history can restore an agent absent from Welcome,
	// just as reconnect history can. Refresh names before either.
	if m.hasCapability("agents") && (m.conversation.agentPending || m.hasCapability("conversation")) {
		m.conversation.agentPending = true
		s.reconnectAgent = m.activeAgent()
		s.agentListID, err = c.Agent(ctx, protocol.AgentRequest{Operation: stringPointer("list")})
	} else if history := m.discoveryHistory(); history != nil && m.hasCapability("conversation") {
		s.reconnectAgent = m.activeAgent()
		s.historyID, err = c.History(ctx, *history)
	}
	return err
}

// Successful history falls through to the shell's history projection. A stale
// agent-list reply is consumed without redrawing, as in the shell dispatcher.
func (s *discoverySession) response(ctx context.Context, c shellClient, peer connectionPeer, e protocol.Envelope) (consumed, redraw bool, err error) {
	m := s.model
	if e.ReplyTo != nil && *e.ReplyTo == s.historyID {
		s.historyID = ""
		if m.conversation.agentPending && m.activeAgent() == s.reconnectAgent && e.Error != nil {
			if m.discoveryFailed(e.Error.Code) {
				peer.Close()
			}
			return true, true, nil
		}
	}
	if e.ReplyTo != nil && *e.ReplyTo == s.agentListID {
		s.agentListID = ""
		if !m.conversation.agentPending || m.activeAgent() != s.reconnectAgent {
			return true, false, nil
		}
		p, ok := e.Payload.(protocol.AgentResult)
		if e.Error != nil {
			if m.discoveryFailed(e.Error.Code) {
				peer.Close()
			}
		} else if !ok || p.Agents == nil || p.Operation == nil || *p.Operation != "list" {
			return true, false, errStream
		} else {
			m.content.Agents = append([]protocol.Agent(nil), (*p.Agents)...)
			for i := range m.content.Agents {
				m.content.Agents[i].Name = sanitize.Display(m.content.Agents[i].Name)
			}
			if history := m.reconnectHistory(m.content.Agents); history != nil {
				if c != nil && m.hasCapability("conversation") {
					s.reconnectAgent = m.activeAgent()
					s.historyID, err = c.History(ctx, *history)
					if err != nil {
						return true, false, err
					}
				} else {
					m.agentDiscoveryDone()
				}
			}
		}
		return true, true, nil
	}
	return false, false, nil
}
