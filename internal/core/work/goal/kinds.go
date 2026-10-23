// SPDX-License-Identifier: Apache-2.0
package goal

type kindSpec struct {
	worker         bool
	externalEffect bool
}

// Every ActionKind has an entry, including kinds without either property.
// TestEveryActionKindHasSpec checks coverage independently of this table.
var kindSpecs = map[ActionKind]kindSpec{
	Assign:         {worker: true, externalEffect: true},
	Resume:         {worker: true, externalEffect: true},
	InspectPR:      {},
	Merge:          {externalEffect: true},
	RunQA:          {externalEffect: true},
	RepairConflict: {worker: true, externalEffect: true},
	RepairQA:       {worker: true, externalEffect: true},
	Deploy:         {externalEffect: true},
	Verify:         {},
	Observe:        {},
	Decide:         {},
	PlanGoal:       {},
	CheckCriterion: {},
	ContinueWorker: {worker: true, externalEffect: true},
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
