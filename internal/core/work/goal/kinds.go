// SPDX-License-Identifier: Apache-2.0
package goal

type kindSpec struct {
	worker         bool
	externalEffect bool
	priorStage     TaskStage
	counter        func(*Task) *int
}

// Every ActionKind has an entry, including kinds without either property.
// TestEveryActionKindHasSpec checks coverage independently of this table.
var kindSpecs = map[ActionKind]kindSpec{
	Assign:         {worker: true, externalEffect: true, priorStage: TaskReady},
	Resume:         {worker: true, externalEffect: true, priorStage: TaskWorkerLost, counter: func(t *Task) *int { return &t.Recoveries }},
	InspectPR:      {},
	Merge:          {externalEffect: true},
	RunQA:          {externalEffect: true},
	RepairConflict: {worker: true, externalEffect: true, priorStage: TaskConflicted, counter: func(t *Task) *int { return &t.Conflicts }},
	RepairQA:       {worker: true, externalEffect: true, priorStage: TaskQAFailed, counter: func(t *Task) *int { return &t.QARepairs }},
	Deploy:         {externalEffect: true},
	Verify:         {},
	Observe:        {},
	Decide:         {},
	PlanGoal:       {},
	CheckCriterion: {},
	ContinueWorker: {worker: true, externalEffect: true, priorStage: TaskNoReceipt, counter: func(t *Task) *int { return &t.Continuations }},
}

// WorkerAction reports whether kind runs a worker and reserves worker capacity.
func WorkerAction(kind ActionKind) bool {
	return kindSpecs[kind].worker
}

func workerAction(kind ActionKind) bool {
	return WorkerAction(kind)
}

func hasExternalEffect(a Action) bool {
	return kindSpecs[a.Kind].externalEffect
}

// Denied and undispatched awards retain their different counter floors and
// settlement rules. Callers supply both policies explicitly.
func restoreWorkerAward(t *Task, a Action, floorAtZero, effectsSettled bool) {
	if stage := kindSpecs[a.Kind].priorStage; stage != "" {
		t.Stage = stage
	}
	if a.Kind == Assign {
		t.NodeID = ""
	}
	if !reserves(a) {
		return
	}
	refundRecovery(t, a.Kind, floorAtZero)
	t.Attempt--
	if floorAtZero {
		t.Attempt = max(0, t.Attempt)
	}
	t.EffectsSettled = effectsSettled
}

// Cancellation before dispatch and a settled quota interruption both leave the
// task's one recovery available. A quota attempt still retains its attempt id.
func refundRecovery(t *Task, kind ActionKind, floorAtZero bool) {
	if counter := kindSpecs[kind].counter; counter != nil {
		value := counter(t)
		*value--
		if floorAtZero {
			*value = max(0, *value)
		}
	}
}
