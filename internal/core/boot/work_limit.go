// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// WorkLimit records the person's run limit choice. The limit is durable in the
// work ledger and takes effect on the next scheduling pass, so the reply reports
// the committed limit and dispatches nothing. A choice that changes nothing
// records no command and answers no applied effect.
func (r *conversationRuntime) WorkLimit(ctx context.Context, s clientsrv.Session, p protocol.WorkLimitRequest, messageID string) (result protocol.WorkLimitResult, applied bool, err error) {
	if !s.CanDecide || s.PersonID != r.person || messageID == "" {
		return result, false, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	runtime := r.workEngine
	if runtime == nil && r.router != nil {
		runtime = r.router.root.workEngine
	}
	if runtime == nil || runtime.engine == nil {
		return result, false, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	record, err := runtime.engine.CurrentRunLimit(ctx, s.PersonID)
	if err != nil {
		return result, false, err
	}
	current := record.Current
	limit := goal.RunLimit{Automatic: true}
	switch p.Operation {
	case "limit_auto":
		if p.Value != nil {
			return result, false, malformedManagement()
		}
	case "limit_up", "limit_down":
		if p.Value != nil {
			return result, false, malformedManagement()
		}
		// A change from automatic starts at the limit that pass applied, so the
		// person moves one step from what they see.
		value := current
		if value < 1 {
			value = 1
		}
		if p.Operation == "limit_up" {
			value++
		} else {
			value--
		}
		if value < 1 || value > goal.MaxRunLimit {
			return result, false, malformedManagement()
		}
		limit = goal.RunLimit{Value: value}
	case "limit_set":
		if p.Value == nil {
			return result, false, malformedManagement()
		}
		value, convErr := strconv.Atoi(string(*p.Value))
		if convErr != nil || value < 1 || value > goal.MaxRunLimit {
			return result, false, malformedManagement()
		}
		limit = goal.RunLimit{Value: value}
	default:
		return result, false, malformedManagement()
	}
	committed, changed, err := runtime.engine.SetRunLimit(ctx, s.PersonID, limit, messageID)
	if err != nil {
		return result, false, workLimitError(err)
	}
	// The reply reports what the next pass will apply: the number the person
	// set, or for automatic the value the last pass applied until a new pass
	// recomputes it.
	answer := goal.RunLimitRecord{RunLimit: committed.RunLimit, Current: committed.Value, Reason: string(goal.ReasonSlot)}
	if committed.Automatic {
		answer.Current, answer.Reason = max(1, current), committed.Reason
		if answer.Reason == "" {
			answer.Reason = string(goal.ReasonLoad)
		}
	}
	return protocol.WorkLimitResult{Automatic: answer.Automatic, Limit: number(answer.Current), Reason: protocol.RunLimitReason(answer.Reason)}, changed, nil
}

// CurrentRunLimit reports the applied limit for the progress payload. A work
// engine that is not running reports no limit rather than a guessed one.
func (r *conversationRuntime) CurrentRunLimit(ctx context.Context) *protocol.RunLimitState {
	runtime := r.workEngine
	if runtime == nil && r.router != nil {
		runtime = r.router.root.workEngine
	}
	if runtime == nil || runtime.engine == nil {
		return nil
	}
	record, err := runtime.engine.CurrentRunLimit(ctx, r.person)
	if err != nil || record.Current < 1 {
		return nil
	}
	return &protocol.RunLimitState{Automatic: record.Automatic, Value: number(record.Current), Reason: protocol.RunLimitReason(record.Reason)}
}

func workLimitError(err error) error {
	switch {
	case errors.Is(err, goal.ErrInvalid):
		return malformedManagement()
	case errors.Is(err, goal.ErrConflict):
		return &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	return err
}
