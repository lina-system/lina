// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// LiveSessions returns the worker handles whose sessions may stay live: the
// steps whose worker turn is still running and the steps under pull-request
// operation. A landed or closed step leaves the set, so Core ends its process
// without closing its thread. The set comes from the ledger alone; the same
// ledger gives the same set every time.
//
// A turn is running while its worker action is claimed, running or uncertain.
// A step is under pull-request operation from its delivery until its merge
// lands, including one conflict repair and one pending continuation. A step
// waiting for a person, a landed step and a spent allowance are closed.
func LiveSessions(g Goal, actions []Action) map[string]bool {
	live := map[string]bool{}
	if g.State == GoalDone || g.State == GoalStopped {
		return live
	}
	states := map[string]string{}
	for _, a := range actions {
		if a.GoalID == g.ID {
			states[a.ID] = a.State
		}
	}
	for _, t := range g.Tasks {
		if t.Session == "" {
			continue
		}
		if inFlight(states[t.Session]) || underPullRequest(t) {
			live[t.Session] = true
		}
	}
	return live
}

// A worker action is in flight from its claim until its outcome settles.
// Unknown effects retain the session because its process may still be alive.
func inFlight(state string) bool {
	return state == "claimed" || state == "running" || state == "unknown"
}

func underPullRequest(t Task) bool {
	switch t.Stage {
	case TaskDelivered, TaskNoReceipt:
		return true
	case TaskConflicted:
		return t.Conflicts < 1
	case TaskWorkerLost:
		return t.EffectsSettled && t.Recoveries < 1
	}
	return false
}

// reapSessions ends the worker processes of steps that have left the frontier.
// It reads only committed state, so two reaps over the same ledger end the same
// sessions. A nil registry keeps nothing and ends nothing.
func (e *Engine) reapSessions(ctx context.Context, s State) error {
	if e.cfg.Sessions == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, g := range s.Goals {
		for id := range LiveSessions(g, s.Actions) {
			keep[id] = true
		}
	}
	return e.cfg.Sessions.EndUnlisted(ctx, keep)
}

// LiveSessions reads the current committed set for every goal. It is a pure
// projection: no model call, no adapter, no worker control.
func (e *Engine) LiveSessions(ctx context.Context) (map[string]bool, error) {
	live := map[string]bool{}
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		v, err := readView(ctx, tx)
		if err != nil {
			return err
		}
		var actions []Action
		for _, r := range v.Records("action") {
			var a Action
			if err := json.Unmarshal(r.Body, &a); err != nil {
				return err
			}
			actions = append(actions, a)
		}
		for _, r := range v.Records("goal") {
			var g Goal
			if err := json.Unmarshal(r.Body, &g); err != nil {
				return err
			}
			for id := range LiveSessions(g, actions) {
				live[id] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return live, nil
}
