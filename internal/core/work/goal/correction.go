// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"slices"
)

// Compare inputs, never execution claims. Dependency order is not an input.
func sameTaskDefinition(a, b Task) bool {
	ad, bd := slices.Clone(a.DependsOn), slices.Clone(b.DependsOn)
	slices.Sort(ad)
	slices.Sort(bd)
	return a.ID == b.ID && a.Done == b.Done && a.Repository == b.Repository &&
		a.Base == b.Base && a.Area == b.Area && a.Worker == b.Worker &&
		a.Resources == b.Resources && a.Repairs == b.Repairs && slices.Equal(ad, bd)
}

func correctedTaskChanges(old, next []Task) map[string]bool {
	changed := map[string]bool{}
	byID := map[string]Task{}
	for _, t := range next {
		byID[t.ID] = t
	}
	for _, t := range old {
		n, ok := byID[t.ID]
		if !ok || !sameTaskDefinition(t, n) {
			changed[t.ID] = true
		}
	}
	// Both graphs matter: removing a dependency changes the downstream input
	// too. A superseded repair result is also an input of its original task.
	all := append(slices.Clone(old), next...)
	for again := true; again; {
		again = false
		for _, t := range all {
			if changed[t.ID] {
				continue
			}
			invalidate := t.RepairedBy != "" && changed[t.RepairedBy]
			for _, dep := range t.DependsOn {
				invalidate = invalidate || changed[dep]
			}
			if invalidate {
				changed[t.ID], again = true, true
			}
		}
	}
	return changed
}

func applyCorrectedPlan(g *Goal, tasks []Task) error {
	next := *g
	next.Tasks = slices.Clone(tasks)
	next.DefinitionPending = false
	for _, t := range tasks {
		if !freshTask(t) {
			return ErrInvalid
		}
		if t.Repairs != "" {
			parent := goalTask(&next, t.Repairs)
			if parent == nil || parent.ID == t.ID || parent.Repository != t.Repository || parent.Base != t.Base {
				return ErrInvalid
			}
		}
	}
	changed := correctedTaskChanges(g.Tasks, tasks)
	for i, t := range next.Tasks {
		if old := goalTask(g, t.ID); old != nil && !changed[t.ID] {
			next.Tasks[i] = *old
		}
	}
	// Keep repair relationships for retained, still-valid repair tasks.
	for _, t := range next.Tasks {
		if t.Repairs != "" {
			goalTask(&next, t.Repairs).RepairedBy = t.ID
		}
	}
	next.Revision++
	next.ManagerTurns++
	next.Checks = nil
	next.Deployment, next.Verification = Proof{}, Proof{}
	next.AttentionID, next.BlockedReason = "", ""
	next.Criteria = goalCriteria(next)
	if err := validateGoal(next); err != nil {
		return err
	}
	*g = next
	return nil
}

// Snapshot the old task on each exact action before replacing the plan. This
// keeps observation and reservations durable across ID reuse and further edits.
// No filesystem operation belongs to plan reconciliation.
func retireCorrectedActions(v *View, g Goal, tasks []Task) error {
	changed := correctedTaskChanges(g.Tasks, tasks)
	for _, r := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(r.Body, &a); err != nil {
			return err
		}
		if a.GoalID != g.ID || a.State == "finished" || a.RetiredTask != nil || !changed[a.TaskID] {
			continue
		}
		if t := goalTask(&g, a.TaskID); t != nil {
			copy := *t
			a.RetiredTask = &copy
			if err := put(v, "action", a.ID, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// Retired effects are read to settlement, never used to advance the new plan.
// The receipt remains on the original action, including failed/partial output.
func reportRetired(a *Action, o Observation) error {
	switch o.Status {
	case OutcomeRunning:
		if o.Handle == "" {
			return ErrInvalid
		}
		a.State, a.Handle = "running", o.Handle
	case OutcomeUnknown:
		a.State = "unknown"
	case OutcomeNeedsPerson:
		a.State = "needs_person"
	case OutcomeSucceeded:
		settled := !reserves(*a) || o.EffectsSettled
		if a.Kind == Merge {
			settled = o.Head == a.Revision && o.Proof.proves(o.Proof.Revision)
		}
		a.State = "unknown"
		if settled {
			a.State = "finished"
		}
	case OutcomeFailed, OutcomeQuota, OutcomeNoReceipt:
		if (reserves(*a) || a.Kind == Merge) && !o.EffectsSettled {
			a.State = "unknown"
		} else {
			a.State = "finished"
		}
	case OutcomeConflict:
		if a.Kind != Merge {
			return ErrInvalid
		}
		a.State = "finished"
	default:
		return ErrInvalid
	}
	return nil
}
