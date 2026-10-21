// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/protocol"
	"time"
)

// The installed main belongs to one person. Both participating agents must
// still be active before any exchange content is projected to that person.
func (r *conversationRuntime) talkAgents(ctx context.Context, from, to string) bool {
	_, a := r.agents.Get(ctx, from)
	_, b := r.agents.Get(ctx, to)
	return a == nil && b == nil
}
func (r *conversationRuntime) Talk(ctx context.Context, s clientsrv.Session, p protocol.AgentTalkGet) (protocol.AgentTalk, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.AgentTalk{}, err
	}
	if p.AgentID != nil {
		if _, err := r.agents.Get(ctx, *p.AgentID); err != nil {
			return protocol.AgentTalk{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
		}
	}
	rows, err := exchange.NewWithMaterials(r.db, r.materials).Log(ctx)
	if err != nil {
		return protocol.AgentTalk{}, err
	}
	entries := []protocol.AgentTalkLine{}
	for _, row := range rows {
		if !r.talkAgents(ctx, row.From, row.To) || p.AgentID != nil && row.From != *p.AgentID && row.To != *p.AgentID {
			continue
		}
		entries = append(entries, protocol.AgentTalkLine{ExchangeID: row.ID, FromAgent: row.From, ToAgent: row.To, Subject: row.Subject, State: string(row.State), Outcome: string(row.Outcome), NeedsPerson: row.NeedsPerson, UpdatedAt: row.At.Format(time.RFC3339Nano)})
	}
	return protocol.AgentTalk{Entries: &entries}, nil
}
func (r *conversationRuntime) ExpandTalk(ctx context.Context, s clientsrv.Session, p protocol.AgentTalkExpand) (protocol.AgentTalk, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.AgentTalk{}, err
	}
	store := exchange.NewWithMaterials(r.db, r.materials)
	row, err := store.Get(ctx, *p.ExchangeID)
	if err != nil || !r.talkAgents(ctx, row.From, row.To) {
		return protocol.AgentTalk{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	events, err := store.Events(ctx, row.ID)
	if err != nil {
		return protocol.AgentTalk{}, err
	}
	result := []protocol.AgentTalkEvent{}
	for _, e := range events {
		result = append(result, protocol.AgentTalkEvent{AgentID: e.Agent, At: e.At.Format(time.RFC3339Nano), Kind: e.Kind, Text: e.Message.Body()})
	}
	return protocol.AgentTalk{ExchangeID: &row.ID, Events: &result}, nil
}
