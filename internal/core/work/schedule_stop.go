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
			if err != nil || c.Task.Schedule == nil || c.Task.PersonID != person || conversation != "" && c.Task.ConversationID != conversation {
				return ErrState
			}
			cards = []Card{c}
		}
		for _, c := range cards {
			if c.Task.Schedule == nil || c.Task.PersonID != person || conversation != "" && c.Task.ConversationID != conversation || c.Scheduling.Closed || c.Scheduling.Landed {
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
