// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

// conversationRouter selects data for the existing conversation flow. All
// personas share Core's stores, lane manager, model route and topic service.
type conversationRouter struct {
	root  *conversationRuntime
	mu    sync.Mutex
	views map[string]*conversationRuntime
}

func (s *conversationRouter) view(ctx context.Context, session clientsrv.Session, agent string) (*conversationRuntime, error) {
	if err := s.root.authorize(session, nil); err != nil {
		return nil, err
	}
	if _, err := s.root.agents.Get(ctx, agent); err != nil {
		return nil, managementError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.views[agent]; r != nil {
		return r, nil
	}
	r := s.root
	materials := *r.materials
	materials.actor.AgentID = agent
	view := &conversationRuntime{materials: &materials, assets: r.assets, root: r.root, steering: map[string][]acceptedSteering{}, ctx: r.ctx, db: r.db, accounts: r.accounts, vault: r.vault, agents: r.agents, lanes: r.lanes, topics: r.topics, route: r.route, activities: r.activities, person: r.person, agent: agent, conversation: "main:" + agent, removals: r.removals}
	if err := view.seedHistory(ctx); err != nil {
		return nil, err
	}
	s.views[agent] = view
	return view, nil
}
func (s *conversationRouter) History(ctx context.Context, session clientsrv.Session, p protocol.HistoryGet) (protocol.History, error) {
	agent := s.root.agent
	if p.AgentID != nil {
		agent = *p.AgentID
	} else if p.ConversationID != nil {
		agent = strings.TrimPrefix(*p.ConversationID, "main:")
	} else if _, err := s.root.agents.Get(ctx, agent); err != nil {
		rows, err := s.root.agents.List(ctx)
		if err != nil {
			return protocol.History{}, managementError(err)
		}
		if len(rows) == 0 {
			return protocol.History{}, outsideManagement()
		}
		agent = rows[0].ID
	}
	r, err := s.view(ctx, session, agent)
	if err != nil {
		return protocol.History{}, err
	}
	return r.History(ctx, session, p)
}
func (s *conversationRouter) Input(ctx context.Context, session clientsrv.Session, p protocol.Input) (protocol.ItemDone, error) {
	if p.ConversationID == nil {
		return protocol.ItemDone{}, malformedManagement()
	}
	r, err := s.view(ctx, session, strings.TrimPrefix(*p.ConversationID, "main:"))
	if err != nil {
		return protocol.ItemDone{}, err
	}
	return r.Input(ctx, session, p)
}
func (s *conversationRouter) Interrupt(ctx context.Context, session clientsrv.Session, p protocol.Interrupt) error {
	if p.ConversationID == nil {
		return malformedManagement()
	}
	r, err := s.view(ctx, session, strings.TrimPrefix(*p.ConversationID, "main:"))
	if err != nil {
		return err
	}
	return r.Interrupt(ctx, session, p)
}

// keep the adapter tied to the one existing ledger flow.
var _ clientsrv.ConversationService = (*conversationRouter)(nil)
