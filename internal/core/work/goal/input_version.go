// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"reflect"
)

// InputVersion is fixed by Claim before dispatch. Signals change the ledger's
// generation, never this token. A recovered read retains the same fence; a new
// read gets a new action identity and cannot make a late old report current.
type InputVersion struct {
	GoalRevision   int64             `json:"goal_revision"`
	RecordRevision string            `json:"record_revision"`
	PRGenerations  map[string]uint64 `json:"pr_generations"`
	TargetRevision string            `json:"target_revision,omitempty"`
	CheckRound     int64             `json:"check_round,omitempty"`
}

func inputVersion(g Goal, a Action) *InputVersion {
	v := &InputVersion{GoalRevision: g.Revision, RecordRevision: g.RecordRevision, PRGenerations: map[string]uint64{}}
	for _, t := range g.Tasks {
		if a.TaskID == "" || a.TaskID == t.ID {
			v.PRGenerations[t.ID] = t.PRGeneration
		}
	}
	switch a.Kind {
	case CheckCriterion:
		v.CheckRound = g.CheckRound
		if a.Check != nil {
			v.TargetRevision = criterionRevision(g, Criterion{Check: *a.Check})
		}
	case Deploy, Verify:
		v.TargetRevision = g.TargetRevision
	case InspectPR, Merge:
		if t := goalTask(&g, a.TaskID); t != nil {
			v.TargetRevision = t.Head
		}
	case RunQA, PlanGoal:
		if t := goalTask(&g, a.TaskID); t != nil {
			v.TargetRevision = t.Merge.Revision
		}
	case Assign, Resume, RepairConflict, RepairQA, ContinueWorker, Observe, Decide:
	}
	return v
}

func changedInputs(g Goal, a Action) bool {
	// A legacy claim without a dispatch token cannot prove current inputs.
	return a.InputVersion == nil || !reflect.DeepEqual(a.InputVersion, inputVersion(g, a))
}

func staleInputs(g Goal, a Action) bool {
	return changedInputs(g, a) || !currentActionTarget(g, a)
}

func currentActionTarget(g Goal, a Action) bool {
	switch a.Kind {
	case CheckCriterion, Deploy, Verify, InspectPR, Merge, RunQA:
		return a.Revision == inputVersion(g, a).TargetRevision
	case PlanGoal:
		if a.TaskID != "" {
			return a.Revision == inputVersion(g, a).TargetRevision
		}
		return !g.DefinitionPending || a.Revision == g.RecordRevision
	case Assign, Resume, RepairConflict, RepairQA, ContinueWorker, Observe, Decide:
		return true
	}
	return false
}

// Settlement is only permission to stop reading an obsolete effect. It is
// never permission to apply its old proof to the current goal.
func effectSettled(a Action, o Observation) bool {
	if o.EffectsSettled {
		return true
	}
	if a.Kind == Merge && o.Status == OutcomeConflict {
		return true
	}
	if o.Status != OutcomeSucceeded && o.Status != OutcomeFailed {
		return false
	}
	switch a.Kind {
	case Deploy, RunQA:
		return o.Proof.Ref != "" && o.Proof.Revision == a.Revision
	case Merge:
		return o.Status == OutcomeSucceeded && o.Head == a.Revision && o.Proof.proves(o.Proof.Revision)
	case Assign, Resume, RepairConflict, RepairQA, ContinueWorker, InspectPR, Verify, Observe, Decide, PlanGoal, CheckCriterion:
		return false
	}
	return false
}

func hasExternalEffect(a Action) bool {
	switch a.Kind {
	case Assign, Resume, RepairConflict, RepairQA, ContinueWorker, Merge, RunQA, Deploy:
		return true
	case InspectPR, Verify, Observe, Decide, PlanGoal, CheckCriterion:
		return false
	}
	return false
}

// Report only retains superseded history. Derivation then releases an unused
// reservation, redoes a pure judgment, or reads an already dispatched effect.
// Reading uses the original effect key; it never spends a worker recovery or
// bypasses authorization by executing a replacement operation.
func (e *Engine) reconcileRetired(v *View) error {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return err
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
		if !a.Superseded || a.RetirementReconciled {
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
