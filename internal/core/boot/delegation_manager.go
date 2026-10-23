// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func (r *conversationRuntime) scheduledAssignment(ctx context.Context, task work.Task) (codingAssignment, error) {
	var a codingAssignment
	err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, task.ID); return err })
	if err != nil {
		return a, err
	}
	if task.Schedule == nil || a.ID != task.ID || a.Tool != "schedule_work" || a.Target != task.Schedule.Repository ||
		a.Owner != task.AgentID || a.Conversation != task.ConversationID || !a.Delegated ||
		a.Origin.Person != task.PersonID || a.Origin.Agent == "" || a.Origin.Conversation == "" || a.Origin.Turn != "turn:"+task.Schedule.InputID {
		return a, work.ErrState
	}
	return a, nil
}

func (r *conversationRuntime) scheduledOrigin(ctx context.Context, task work.Task) (dispatch.Origin, error) {
	a, err := r.scheduledAssignment(ctx, task)
	if errors.Is(err, sql.ErrNoRows) && task.Schedule != nil {
		// Legacy records retain their original scope for reads/cancellation.
		// Execution separately requires a durable manager judgment.
		return dispatch.Origin{Person: task.PersonID, Agent: task.AgentID, Conversation: task.ConversationID, Turn: "turn:" + task.Schedule.InputID}, nil
	}
	return a.Origin, err
}

func (r *conversationRuntime) scheduledDeliveryMatches(ctx context.Context, task work.Task) bool {
	origin, err := r.scheduledOrigin(ctx, task)
	return err == nil && origin.Person == r.person && origin.Agent == r.agent && origin.Conversation == r.conversation
}
