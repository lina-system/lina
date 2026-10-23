// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// StopScheduled closes admission and conflict returns without claiming a forge
// close or a worker termination. Unknown effects retain their reservations.
// An empty reference selects all scheduled cards in the authenticated scope.
func (s *Service) StopScheduled(ctx context.Context, person, conversation, reference string) (count int, err error) {
	if person == "" {
		return 0, ErrInvalid
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		cards, err := cardsIn(ctx, tx)
		if err != nil {
			return err
		}
		if reference != "" {
			c, err := ReadReference(ctx, tx, reference)
			if err != nil || c.Task.Schedule == nil || c.Task.PersonID != person {
				return ErrState
			}
			matches, err := scheduledConversationMatches(ctx, tx, c.Task, conversation)
			if err != nil {
				return err
			}
			if !matches {
				return ErrState
			}
			cards = []Card{c}
		}
		for _, c := range cards {
			if c.Task.Schedule == nil || c.Task.PersonID != person || c.Scheduling.Closed || c.Scheduling.Landed {
				continue
			}
			matches, err := scheduledConversationMatches(ctx, tx, c.Task, conversation)
			if err != nil {
				return err
			}
			if !matches {
				continue
			}
			state := c.Scheduling
			state.Closed = true
			// A reserved start with no reply is also an unknown effect. Only
			// an already settled execution can release its area here.
			if !state.CapacityHeld {
				state.AreaHeld = false
			}
			if err := schedulingEvent(ctx, tx, c, "schedule_stopped", state, nil); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return
}

// Manager ownership does not remove the originating conversation's stop scope.
// The retained binding must still name this exact person, owner and task.
func scheduledConversationMatches(ctx context.Context, tx *canon.Tx, task Task, conversation string) (bool, error) {
	if conversation == "" || task.ConversationID == conversation {
		return true, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM coding_assignments WHERE id=?
		AND json_extract(record,'$.Owner')=? AND json_extract(record,'$.Conversation')=?
		AND json_extract(record,'$.Tool')='schedule_work' AND json_extract(record,'$.Delegated')=1
		AND json_extract(record,'$.Origin.Person')=? AND json_extract(record,'$.Origin.Conversation')=?`,
		task.ID, task.AgentID, task.ConversationID, task.PersonID, conversation).Scan(&count)
	return count == 1, err
}
