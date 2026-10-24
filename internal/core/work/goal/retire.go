// SPDX-License-Identifier: Apache-2.0
package goal

import "encoding/json"

// A goal revision invalidates undispatched actions, including their reservations and
// one-shot planning/recovery counters. They have never been dispatched. Claimed
// actions retain their receipts and reservations until their effects settle.
func retireUndispatchedActions(v *View, g *Goal) error {
	for _, r := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(r.Body, &a); err != nil {
			return err
		}
		if a.GoalID != g.ID || a.ObserveActionID != "" || (a.State != "queued" && a.State != "awaiting_authorization") || a.GoalRevision == g.Revision {
			continue
		}
		restoreUndispatchedTask(g, a)
		a.State = "finished"
		a.Superseded, a.RetirementReconciled = true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}

// Reports retain superseded history and clear obsolete assessment markers.
// Derivation completes retirement, releases an unused reservation, redoes a
// pure judgment, or reads an already dispatched effect.
// Reading uses the original effect key; it never spends a worker recovery or
// bypasses authorization by executing a replacement operation.
func (e *Engine) reconcileRetired(v *View) error {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return err
		}
		// Only queued actions can become stale here, and only unreconciled
		// superseded outcomes need retirement. Loading every running action's
		// whole graph makes a parallel frontier repeatedly decode the same goal
		// while Core is trying to publish the step that just landed.
		if a.State != "queued" && (a.State != "finished" || !a.Superseded || a.RetirementReconciled) {
			continue
		}
		g, err := loadGoal(v, a.GoalID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		if a.State == "queued" && (changedInputs(g, a) || a.ObserveActionID == "" && !currentActionTarget(g, a)) {
			a.State, a.Superseded = "finished", true
		}
		if a.State != "finished" || !a.Superseded || a.RetirementReconciled {
			continue
		}
		t := goalTask(&g, a.TaskID)
		var receipt Observation
		_ = json.Unmarshal(a.Receipt, &receipt)
		if a.Epoch == 0 && a.ObserveActionID == "" || settledRefusal(a) {
			restoreDeniedAssignment(t, a)
		} else if hasExternalEffect(a) && (currentActionTarget(g, a) || !effectSettled(a, receipt)) {
			// Use a distinct report key so a delayed old completion cannot be
			// accepted under the new read's token. Keep the original effect key.
			next := a
			if next.ObserveActionID == "" {
				next.ObserveActionID = a.ID
			}
			if receipt.Handle != "" {
				next.Handle = receipt.Handle
			}
			next.Superseded, next.RetirementReconciled = false, false
			next.Receipt = nil
			next.State, next.Epoch = "queued", 0
			next.ObservedAt, next.ObserverEpoch = 0, 0
			next.GoalRevision = g.Revision
			next.InputVersion = inputVersion(g, next)
			next.Sequence++
			next.ID = actionID(next)
			if err = put(v, "action", next.ID, next); err != nil {
				return err
			}
		}
		if a.Kind == PlanGoal && t != nil && t.RepairedBy == "" {
			t.RepairAsked = false
		}
		a.RetirementReconciled = true
		if err = put(v, "action", a.ID, a); err != nil {
			return err
		}
		if err = put(v, "goal", g.ID, g); err != nil {
			return err
		}
	}
	return nil
}

// The correction barrier has settled these effects. Keep their receipts, but
// exclude them from the replacement's evidence without scheduling another read.
func retireCorrectedTaskReceipts(v *View, goalID string, changed map[string]bool) error {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return err
		}
		if a.GoalID != goalID || !changed[a.TaskID] || a.State != "finished" {
			continue
		}
		a.Superseded, a.RetirementReconciled = true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}

func retireReportedAction(v *View, g Goal, a Action, o Observation) error {
	t := goalTask(&g, a.TaskID)
	var err error
	// Retain the obsolete response as history, never as current evidence
	// or a plan for this revision. A late result is not a Core failure.
	a.State = "finished"
	a.Superseded = true
	if a.Kind == PlanGoal && !a.RetirementReconciled {
		// Clear the assessment with its receipt. Reconciliation finishes
		// retirement before deriving a replacement; later reports must
		// not clear that replacement's marker.
		if t != nil && t.RepairedBy == "" {
			t.RepairAsked = false
			if err = put(v, "goal", g.ID, g); err != nil {
				return err
			}
		}
	}
	if !a.RetirementReconciled && hasExternalEffect(a) && !currentActionTarget(g, a) && !effectSettled(a, o) {
		// A different target cannot inherit this effect. Retain its
		// lease and bounded observation wait until its outcome is known.
		a.State = "unknown"
		if o.Handle != "" {
			a.Handle = o.Handle
		}
	}
	return put(v, "action", a.ID, a)
}

func (e *Engine) retireRevisedActions(v *View, current *Goal) error {
	var err error
	for _, r := range v.Records("action") {
		var a Action
		if err = json.Unmarshal(r.Body, &a); err != nil {
			return err
		}
		if a.GoalID != current.ID || a.State == "finished" {
			continue
		}
		retire := a.State == "queued" || revisionJudgment(a)
		if a.State == "needs_person" {
			if revisionJudgment(a) || settledRefusal(a) {
				restoreDeniedAssignment(goalTask(current, a.TaskID), a)
				retire = true
			} else {
				// An operation may already have started before asking for
				// help. Revision cannot release that lease or repeat it.
				a.State = "unknown"
				a.Deadline = e.cfg.Now().Unix()
			}
		}
		if retire {
			a.State = "finished"
			a.Superseded = true
			if t := goalTask(current, a.TaskID); a.Kind == PlanGoal && t != nil && t.RepairedBy == "" {
				t.RepairAsked = false
				a.RetirementReconciled = true
			}
		}
		if err = put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}

func retireSignaledMerge(a *Action) bool {
	if a.State == "queued" && a.Kind == Merge && a.ObserveActionID == "" {
		a.State = "finished"
		return true
	}
	return false
}
