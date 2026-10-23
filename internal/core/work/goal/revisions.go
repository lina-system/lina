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
		if old := goalTask(g, task.ID); old != nil {
			if !changed[task.ID] {
				proposed.Tasks[i] = *old
			} else {
				// Attempts identify effects, not the definition's recovery budget.
				proposed.Tasks[i].Attempt = old.Attempt
			}
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
	if proposed.ServiceTarget == "" {
		proposed.TargetRevision = correctedLibraryTarget(proposed)
	}

	proposed.AssessedFailure = ""
	proposed.AttentionID = ""
	proposed.BlockedReason = ""
	proposed.State = GoalRunning
	*g = proposed
	return nil
}

// Library verification reads every retained task's QA proof. Its representative
// revision must still belong to that plan; historical repository merges remain
// unchanged because retiring a task does not undo the merge.
func correctedLibraryTarget(g Goal) string {
	var target string
	for _, task := range g.Tasks {
		if !task.Merge.proves(task.Merge.Revision) {
			continue
		}
		revision := taskResultRevision(task)
		if revision == g.TargetRevision {
			return revision
		}
		if target == "" {
			target = revision
		}
	}
	return target
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
	restoreWorkerAward(t, a, true, a.Kind != Assign)
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
