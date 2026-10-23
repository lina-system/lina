// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type a2aListSource struct{}

func (a2aListSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM a2a_calls a WHERE person=? AND NOT EXISTS(SELECT 1 FROM a2a_calls b WHERE json_extract(b.record,'$.parent')=a.id)`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, "a2a:"+id)
	}
	return ids, rows.Err()
}
func (a2aListSource) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	var raw []byte
	var c a2a.Call
	if err := tx.QueryRowContext(ctx, "SELECT record FROM a2a_calls WHERE id=?", strings.TrimPrefix(id, "a2a:")).Scan(&raw); err != nil {
		return runs.ListItem{}, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return runs.ListItem{}, err
	}
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM agents WHERE id=? AND trashed_at=0", c.Origin.Agent).Scan(&name); err != nil {
		return runs.ListItem{}, err
	}
	state := "unknown"
	switch c.State {
	case "working":
		state = "working"
	case "input_required", "auth_required":
		state = "needs_input"
	case "verified":
		state = "complete"
	case "unverified":
		state = "partial"
	case "canceled":
		state = "cancelled"
	case "failed":
		state = "failed"
	}
	return runs.ListItem{Person: c.Origin.Person, Agent: c.Origin.Agent, AgentName: name, Summary: "Remote agent: " + c.Connection, State: state, Started: c.Deadline.Add(-10 * time.Minute)}, nil
}
func (r *conversationRuntime) a2aRunningSummary(ctx context.Context, person, id string) (string, error) {
	calls, err := r.remoteAgents.Calls(ctx, person)
	if err != nil {
		return "", err
	}
	for _, c := range calls {
		if "a2a:"+c.ID == id {
			return "Remote agent: " + c.Connection + "\n" + c.State + "\n" + c.Question, nil
		}
	}
	return "", a2a.ErrScope
}
func (r *conversationRuntime) stopA2A(ctx context.Context, s clientsrv.Session, p protocol.Stop, messageID string) (clientsrv.Result, error) {
	calls, err := r.remoteAgents.Calls(ctx, s.PersonID)
	if err != nil {
		return clientsrv.Result{}, err
	}
	for _, c := range calls {
		if "a2a:"+c.ID != *p.RunID {
			continue
		}
		if p.ConversationID != nil && *p.ConversationID != c.Origin.Conversation {
			return clientsrv.Result{}, outsideManagement()
		}
		v, err := r.router.view(ctx, s, c.Origin.Agent)
		if err != nil {
			return clientsrv.Result{}, err
		}
		bindings := map[string]loop.Tool{}
		if _, err = v.prepareA2ATools(c.Origin.Turn, nil, bindings); err != nil {
			return clientsrv.Result{}, err
		}
		raw, _ := json.Marshal(remoteArgs{Operation: "cancel", Call: c.ID})
		result := bindings["remote_agent"].Execute(ctx, provider.ToolCall{ID: messageID, Name: "remote_agent", Arguments: string(raw)})
		if result.Code != loop.OK && result.Code != loop.Unknown {
			return clientsrv.Result{}, outsideManagement()
		}
		return clientsrv.Result{Kind: protocol.KindStopResult, Effect: protocol.EffectApplied, Payload: protocol.StopResult{RunID: *p.RunID, Outcome: protocol.StopOutcomeRequested}}, nil
	}
	return clientsrv.Result{}, outsideManagement()
}
