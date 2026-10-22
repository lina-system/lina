// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

// activityMu serializes phase changes with concurrent tool completions and the
// person's interrupt. This display state never enters the durable conversation.
func (r *conversationRuntime) setActive(ctx context.Context, active bool) error {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	r.turnActive = active
	r.turnPhase = ""
	r.toolNames = nil
	r.toolOrder = nil
	if active {
		return r.phaseLocked(ctx, protocol.TurnPhaseWaitingModel, "")
	}
	return r.publish(ctx, protocol.KindTurnState, "turn", protocol.TurnState{Active: &active})
}

func (r *conversationRuntime) phaseLocked(ctx context.Context, phase protocol.TurnPhase, name string) error {
	if !r.turnActive || r.turnPhase == protocol.TurnPhaseStopping && phase != protocol.TurnPhaseStopping {
		return nil
	}
	if r.turnPhase == phase && r.currentTool == name {
		return nil
	}
	r.turnPhase, r.currentTool = phase, name
	active := true
	payload := protocol.TurnState{Active: &active, Phase: &phase}
	if name != "" {
		payload.ToolName = &name
	}
	return r.publish(ctx, protocol.KindTurnState, "turn", payload)
}
func (r *conversationRuntime) setPhase(ctx context.Context, phase protocol.TurnPhase) error {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	return r.phaseLocked(ctx, phase, "")
}
func (r *conversationRuntime) toolActivity(ctx context.Context, activity turn.ToolActivity) error {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	name := tools.DisplayName(activity.ToolName)
	payload := protocol.ToolActivity{CallID: activity.CallID, ToolName: name, Status: protocol.ToolActivityStatusStarted}
	if activity.Started {
		if r.toolNames == nil {
			r.toolNames = map[string]string{}
		}
		r.toolNames[activity.CallID] = name
		r.toolOrder = append(r.toolOrder, activity.CallID)
		if err := r.phaseLocked(ctx, protocol.TurnPhaseTool, name); err != nil {
			return err
		}
	} else {
		payload.Status = protocol.ToolActivityStatusFinished
		outcome := protocol.ToolActivityOutcomeFailed
		if activity.Code == loop.OK {
			outcome = protocol.ToolActivityOutcomeOk
		}
		duration := json.Number(strconv.FormatInt(max(0, activity.Duration.Milliseconds()), 10))
		payload.Outcome, payload.DurationMs = &outcome, &duration
		delete(r.toolNames, activity.CallID)
		r.toolOrder = slices.DeleteFunc(r.toolOrder, func(id string) bool { return id == activity.CallID })
	}
	if err := r.publish(ctx, protocol.KindToolActivity, "tool:"+activity.CallID, payload); err != nil {
		return err
	}
	if !activity.Started {
		if len(r.toolOrder) > 0 {
			return r.phaseLocked(ctx, protocol.TurnPhaseTool, r.toolNames[r.toolOrder[len(r.toolOrder)-1]])
		}
		return r.phaseLocked(ctx, protocol.TurnPhaseWaitingModel, "")
	}
	return nil
}
