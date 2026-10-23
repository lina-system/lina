// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) installedCodeWork() (*WorkConfig, error) {
	connections, err := readCodeConnections(r.root)
	if err != nil {
		return nil, err
	}
	return &WorkConfig{
		Driver: func(e *goal.Engine) (goal.Driver, error) {
			return &codeGoalDriver{runtime: r, engine: e, connections: connections}, nil
		},
		MainHost: func(ctx context.Context) (goal.Node, error) {
			workers := map[string]bool{"antigravity": r.workerDispatch.WorkerAvailable("antigravity") && len(connections.Repositories) > 0}
			sample, err := r.codeHost(ctx, connections, workers)
			if err != nil {
				return goal.Node{ID: r.main, ObservedAt: time.Now().Unix()}, nil
			}
			return sample, nil
		},
		Changed: r.publishCodeGoals,
	}, nil
}
func (r *conversationRuntime) publishCodeGoals(ctx context.Context, s goal.State) error {
	for _, g := range s.Goals {
		if g.Conversation == "" || g.Turn == "" || (g.State != goal.GoalDone && g.State != goal.GoalBlocked) {
			continue
		}
		// A routed goal belongs to the code owner. Its result still answers the
		// original person input, retained in the durable coding assignment.
		agent, conversation, originTurn := g.Agent, g.Conversation, g.Turn
		if err := r.db.Read(ctx, func(tx *canon.Tx) error {
			a, err := readCoding(ctx, tx, g.ID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if a.Owner != g.Agent || a.Origin.Person != g.Person {
				return goal.ErrInvalid
			}
			agent, conversation, originTurn = a.Origin.Agent, a.Origin.Conversation, a.Origin.Turn
			return nil
		}); err != nil {
			return err
		}
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: g.Person}, agent)
		if err != nil {
			return err
		}
		// The originating conversation, rather than the agent's current default,
		// receives the durable result after a restart or a client disconnect.
		if view.conversation != conversation {
			view = newConversationView(r, agent, conversation)
		}
		id := "goal-result:" + codingID(g.ID, string(g.State), g.RecordRevision)
		if _, err = view.journal().Get(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, ledger.ErrNotFound) {
			return err
		}
		parent, err := view.journal().Get(ctx, strings.TrimPrefix(originTurn, "turn:"))
		if err != nil {
			return err
		}
		text := "Done. " + g.Summary
		if g.State == goal.GoalBlocked {
			text = "This work needs attention. " + g.Summary
		}
		now := time.Now()
		event, err := view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: conversation, ParentID: parent.ID, AgentID: agent, Surface: parent.Surface, ScaffoldKind: "goal_result", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
		if err != nil {
			return err
		}
		text = event.Body.Text()
		speaker := protocol.ConversationSpeakerSystem
		if err = view.publish(ctx, protocol.KindItemDone, id, protocol.ItemDone{Text: &text, Speaker: &speaker}); err != nil {
			return err
		}
	}
	return nil
}
