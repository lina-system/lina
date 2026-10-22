// SPDX-License-Identifier: Apache-2.0
package goal

import "encoding/json"

func revisionJudgment(a Action) bool { return a.Kind == PlanGoal || a.Kind == CheckCriterion }

func staleJudgment(g Goal, a Action) bool {
	if !revisionJudgment(a) {
		return false
	}
	if a.GoalRevision != g.Revision {
		return true
	}
	if a.Kind == PlanGoal {
		return g.DefinitionPending && a.TaskID == "" && a.Revision != g.RecordRevision
	}
	return a.CheckRound != g.CheckRound || a.Check != nil && a.Revision != criterionRevision(g, Criterion{Check: *a.Check})
}

func blockCorrection(g *Goal, reason string) {
	g.State = GoalBlocked
	g.AttentionID = "goal:" + g.ID + ":definition"
	g.BlockedReason = reason
	g.AssessedFailure = "definition:" + g.RecordRevision
}

func applyCorrectionPlan(g *Goal, o Observation) error {
	if len(o.Tasks) == 0 {
		blockCorrection(g, "The corrected goal needs a revised work definition.")
		return nil
	}
	// Keep the persisted correction fenced if this proposal cannot reconcile the
	// retained tasks. Invalid planning data must not stop unrelated goals.
	proposed := *g
	proposed.Tasks = append(append([]Task(nil), g.Tasks...), o.Tasks...)
	proposed.DefinitionPending = false
	proposed.Revision++
	proposed.ManagerTurns++
	proposed.Criteria = goalCriteria(proposed)
	for _, task := range o.Tasks {
		if !freshTask(task) || task.Repairs != "" {
			blockCorrection(g, "The corrected plan needs tasks with fresh execution state.")
			return nil
		}
	}
	if validateGoal(proposed) != nil {
		blockCorrection(g, "The corrected plan does not reconcile retained tasks with the goal scope.")
		return nil
	}
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
