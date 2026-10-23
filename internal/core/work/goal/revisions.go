// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"slices"
)

func revisionJudgment(a Action) bool { return a.Kind == PlanGoal || a.Kind == CheckCriterion }

func blockCorrection(g *Goal, reason string) {
	g.State = GoalBlocked
	g.AttentionID = "goal:" + g.ID + ":definition"
	g.BlockedReason = reason
	g.AssessedFailure = "definition:" + g.RecordRevision
}

func applyCorrectionPlan(g *Goal, o Observation) error {
	if !validServiceRepository(*g) {
		blockCorrection(g, "The service needs a repository in the goal scope.")
		return nil
	}
	// The revision barrier has read all dispatched task effects. Reconcile the
	// manager's complete list, retaining execution state only for unchanged inputs.
	proposed := *g
	proposed.Tasks = slices.Clone(o.Tasks)
	proposed.DefinitionPending = false
	proposed.Revision++
	proposed.ManagerTurns++
	for _, task := range o.Tasks {
		if !freshTask(task) {
			blockCorrection(g, "The corrected plan needs tasks with fresh execution state.")
			return nil
		}
		if task.Repairs != "" {
			parent := goalTask(&proposed, task.Repairs)
			if parent == nil || parent.ID == task.ID || parent.Repository != task.Repository || parent.Base != task.Base {
				blockCorrection(g, "The corrected repair needs its original task in the plan.")
				return nil
			}
		}
	}
	changed := correctedTaskChanges(g.Tasks, o.Tasks)
	for i, task := range proposed.Tasks {
		if old := goalTask(g, task.ID); old != nil && !changed[task.ID] {
			proposed.Tasks[i] = *old
		}
	}
	for _, task := range proposed.Tasks {
		if task.Repairs != "" {
			goalTask(&proposed, task.Repairs).RepairedBy = task.ID
		}
	}
	proposed.Criteria = goalCriteria(proposed)
	if validateGoal(proposed) != nil {
		blockCorrection(g, "The corrected plan does not reconcile retained tasks with the goal scope.")
		return nil
	}
	proposed.Checks = nil
	proposed.Deployment, proposed.Verification = Proof{}, Proof{}

	proposed.AssessedFailure = ""
	proposed.AttentionID = ""
	proposed.BlockedReason = ""
	proposed.State = GoalRunning
	*g = proposed
	return nil
}

func settledRefusal(a Action) bool {
	var receipt Observation
	return json.Unmarshal(a.Receipt, &receipt) == nil && receipt.Status == OutcomeNeedsPerson && receipt.EffectsSettled
}

// Only a refusal with proof of no outstanding effect can release its award.
// Return a denied worker award to its prior stage so the new revision asks the
// driver to authorize a new action; do not spend another recovery on a refusal.
func restoreDeniedAssignment(t *Task, a Action) {
	if t == nil || !reserves(a) || t.Attempt != a.Attempt {
		return
	}
	t.Attempt = max(0, t.Attempt-1)
	if a.Kind == Assign {
		t.Stage = TaskReady
		t.NodeID = ""
	}
	if a.Kind == Resume {
		t.Stage = TaskWorkerLost
		t.Recoveries = max(0, t.Recoveries-1)
	}
	if a.Kind == RepairConflict {
		t.Stage = TaskConflicted
		t.Conflicts = max(0, t.Conflicts-1)
	}
	if a.Kind == ContinueWorker {
		t.Stage = TaskNoReceipt
		t.Continuations = max(0, t.Continuations-1)
	}
	if a.Kind == RepairQA {
		t.Stage = TaskQAFailed
		t.QARepairs = max(0, t.QARepairs-1)
	}
	t.EffectsSettled = a.Kind != Assign
}

// A completed task's result is already included in the plan reconciliation.
// Delayed history must not create a fresh observation against a reused task ID.
func retireCompletedTaskHistory(v *View, goalID string) error {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return err
		}
		if a.GoalID != goalID || a.TaskID == "" || a.State != "finished" {
			continue
		}
		a.Superseded, a.RetirementReconciled = true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}

func correctionEffectsPending(v *View, goalID string) (bool, error) {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return false, err
		}
		if a.GoalID == goalID && a.TaskID != "" && a.Kind != PlanGoal && a.State != "finished" {
			return true, nil
		}
	}
	return false, nil
}
