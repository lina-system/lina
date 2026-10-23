// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// WorkControl records one of the person's choices about a goal. The choice is
// durable in the work ledger and takes effect on the next scheduling pass, so
// the reply reports the committed goal and dispatches nothing. A choice that
// changes nothing reports no applied effect and records no command.
func (r *conversationRuntime) WorkControl(ctx context.Context, s clientsrv.Session, p protocol.WorkControlRequest, messageID string) (result protocol.WorkControlResult, applied bool, err error) {
	if !s.CanDecide || s.PersonID != r.person || messageID == "" {
		return result, false, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	command := goal.Command(p.Operation)
	confirmed := p.Confirm != nil && *p.Confirm
	switch command {
	case goal.CommandStop:
		if p.Confirm == nil {
			return result, false, malformedManagement()
		}
	case goal.CommandRankUp, goal.CommandRankDown, goal.CommandPause, goal.CommandResume:
		if p.Confirm != nil {
			return result, false, malformedManagement()
		}
	default:
		return result, false, malformedManagement()
	}
	runtime := r.workEngine
	if runtime == nil && r.router != nil {
		runtime = r.router.root.workEngine
	}
	if runtime == nil || runtime.engine == nil {
		return result, false, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	g, applied, err := runtime.engine.Command(ctx, goal.GoalCommand{ID: messageID, Goal: p.GoalID, Person: s.PersonID, Command: command, Confirmed: confirmed})
	if err != nil {
		return result, false, workControlError(err)
	}
	result = protocol.WorkControlResult{GoalID: g.ID, Operation: p.Operation, State: string(g.State), Paused: g.Paused}
	result.Rank = json.Number(strconv.Itoa(g.Rank))
	// The ledger commit wakes the progress projection; this reply dispatches
	// nothing and the next scheduling pass applies the choice.
	return result, applied, nil
}

func workControlError(err error) error {
	switch {
	case errors.Is(err, goal.ErrInvalid):
		return malformedManagement()
	case errors.Is(err, goal.ErrNotFound):
		return outsideManagement()
	case errors.Is(err, goal.ErrConflict):
		return &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	return err
}
