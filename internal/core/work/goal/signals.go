// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Signal struct {
	ID       string `json:"id"`
	ActionID string `json:"action_id,omitempty"`
	GoalID   string `json:"goal_id,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
}

// Signal persists an integration's change hint. It carries no instructions or
// outcome claims: dispatch reads the current receipt or PR before progressing.
// An action-id hint for an authorization wait retries Authorize, not approval;
// the trusted authority must still grant the exact action before Execute runs.
func (e *Engine) Signal(ctx context.Context, s Signal) error {
	if s.ID == "" || (s.ActionID == "") == (s.GoalID == "" || s.TaskID == "") {
		return ErrInvalid
	}
	sum := sha256.Sum256(encode(s))
	err := e.journal.Update(ctx, Transition{ID: "signal:" + s.ID, Kind: "integration.changed", At: 1, Data: encode(s)}, func(v *View) error {
		goalID, taskID := s.GoalID, s.TaskID
		if s.ActionID != "" {
			a, err := loadAction(v, s.ActionID)
			if err != nil {
				return err
			}
			if a.TaskID == "" || a.State == "awaiting_authorization" {
				if a.State == "finished" {
					return nil
				}
				a.Signal = hex.EncodeToString(sum[:])
				a.Deadline = e.cfg.Now().Unix()
				if a.State == "awaiting_authorization" {
					g, err := loadGoal(v, a.GoalID)
					if err != nil {
						return err
					}
					if err = e.hydrate(&g); err != nil {
						return err
					}
					// Published authorization waits predate dispatch tokens. This
					// state proves Execute never ran, so retry authority with current
					// inputs instead of observing an effect that does not exist.
					a.Receipt = encode(Observation{Status: OutcomeNeedsPerson, EffectsSettled: true})
					if a.GoalRevision == g.Revision && currentActionTarget(g, a) {
						a.InputVersion = inputVersion(g, a)
					}
					a.State = "queued"
				}
				return put(v, "action", a.ID, a)
			}
			// Integrations retain the original effect key after a stale report
			// is retired. Its signal also fences any replacement receipt read.
			goalID, taskID = a.GoalID, a.TaskID
		}
		g, err := loadGoal(v, goalID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		t := goalTask(&g, taskID)
		if t == nil {
			return ErrInvalid
		}
		if g.State == GoalDone || g.State == GoalStopped {
			return nil
		}
		// Every task hint invalidates the prior observation, even while this PR
		// waits for another task's merge slot and has no queued merge yet.
		t.PR = PRState{}
		t.PRGeneration++
		for _, r := range v.Records("action") {
			var a Action
			if err = json.Unmarshal(r.Body, &a); err != nil {
				return err
			}
			if a.GoalID == g.ID && a.TaskID == t.ID && a.State != "finished" {
				if a.State == "queued" && a.Kind == Merge && a.ObserveActionID == "" {
					a.State = "finished"
				} else {
					a.Signal = hex.EncodeToString(sum[:])
					a.Deadline = e.cfg.Now().Unix()
					if a.State == "queued" && a.Epoch == 0 && a.ObserveActionID == "" && a.GoalRevision == g.Revision && currentActionTarget(g, a) {
						// No dispatch has read these inputs yet. Claim will freeze
						// the current generation before authorization and execution.
						a.InputVersion = inputVersion(g, a)
					}
				}
				if err = put(v, "action", a.ID, a); err != nil {
					return err
				}
			}
		}
		return put(v, "goal", g.ID, g)
	})
	if errors.Is(err, errNoChange) {
		return nil
	}
	return err
}
