// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) Work(ctx context.Context, s clientsrv.Session, p protocol.WorkSwitchRequest, messageID string) (result protocol.WorkSwitchResult, err error) {
	if !s.CanDecide || s.PersonID != r.person {
		return result, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if p.Operation == "approve_switch" {
		if p.Tasks == nil || p.Snapshot == nil || p.Evidence != nil || messageID == "" {
			return result, malformedManagement()
		}
		err = r.work.ApproveSwitch(ctx, work.Switch{ID: p.SwitchID, Owner: s.PersonID, Approval: messageID, Snapshot: *p.Snapshot, Tasks: *p.Tasks})
	} else {
		if p.Tasks != nil || p.Snapshot != nil {
			return result, malformedManagement()
		}
		if p.Operation == "switch_status" {
			if p.Evidence != nil {
				return result, malformedManagement()
			}
		} else {
			if p.Evidence == nil {
				return result, malformedManagement()
			}
			switch p.Operation {
			case "activate_switch":
				err = r.work.ActivateSwitch(ctx, p.SwitchID, s.PersonID, *p.Evidence)
			case "rollback_switch":
				err = r.work.RequestRollback(ctx, p.SwitchID, s.PersonID, *p.Evidence)
			case "release_switch":
				err = r.work.CompleteRollback(ctx, p.SwitchID, s.PersonID, *p.Evidence)
			default:
				return result, malformedManagement()
			}
		}
	}
	if err != nil {
		return result, switchError(err)
	}
	v, err := r.work.GetSwitch(ctx, p.SwitchID)
	if err != nil {
		return result, switchError(err)
	}
	if v.Owner != s.PersonID {
		return result, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	result = protocol.WorkSwitchResult{SwitchID: v.ID, State: v.State, Approval: v.Approval, Snapshot: v.Snapshot, Tasks: v.Tasks, Events: []protocol.WorkSwitchEvent{}}
	for _, e := range v.Events {
		result.Events = append(result.Events, protocol.WorkSwitchEvent{State: e.State, Evidence: e.Evidence})
	}
	return result, nil
}
func switchError(err error) error {
	if errors.Is(err, work.ErrInvalid) {
		return malformedManagement()
	}
	if errors.Is(err, work.ErrNotFound) {
		return outsideManagement()
	}
	if errors.Is(err, work.ErrState) || errors.Is(err, work.ErrActiveRun) || errors.Is(err, work.ErrControlHeld) {
		return &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	return err
}
