// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"go/constant"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestEveryActionKindHasSpec(t *testing.T) {
	t.Parallel()
	// Read the declared constants, not a second hand-maintained list: a new
	// kind must fail this test even if nobody adds it to the expectations below.
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedTypes, Context: t.Context()}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want goal only", len(pkgs))
	}
	for _, err := range pkgs[0].Errors {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	scope := pkgs[0].Types.Scope()
	kindType := scope.Lookup("ActionKind").Type()
	declared := map[ActionKind]bool{}
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*types.Const)
		if !ok || !types.Identical(c.Type(), kindType) {
			continue
		}
		kind := ActionKind(constant.StringVal(c.Val()))
		declared[kind] = true
		if _, ok := kindSpecs[kind]; !ok {
			t.Errorf("ActionKind %s (%q) has no kindSpec", name, kind)
		}
	}
	if len(declared) == 0 {
		t.Fatal("no ActionKind constants found")
	}
	for kind := range kindSpecs {
		if !declared[kind] {
			t.Errorf("kindSpec %q has no ActionKind constant", kind)
		}
	}
}

func TestActionKindClassification(t *testing.T) {
	t.Parallel()
	// Preserve the worker and external-effect sets from before the table.
	want := map[ActionKind]kindSpec{
		Assign:         {worker: true, externalEffect: true},
		Resume:         {worker: true, externalEffect: true},
		RepairConflict: {worker: true, externalEffect: true},
		RepairQA:       {worker: true, externalEffect: true},
		ContinueWorker: {worker: true, externalEffect: true},
		Merge:          {externalEffect: true},
		RunQA:          {externalEffect: true},
		Deploy:         {externalEffect: true},
		InspectPR:      {},
		Verify:         {},
		Observe:        {},
		Decide:         {},
		PlanGoal:       {},
		CheckCriterion: {},
	}
	if len(kindSpecs) != len(want) {
		t.Errorf("kindSpecs has %d entries, want %d", len(kindSpecs), len(want))
	}
	want[""] = kindSpec{}
	want["unknown"] = kindSpec{}
	for kind, spec := range want {
		t.Run(string(kind), func(t *testing.T) {
			if got := WorkerAction(kind); got != spec.worker {
				t.Errorf("WorkerAction = %t, want %t", got, spec.worker)
			}
			if got := workerAction(kind); got != spec.worker {
				t.Errorf("workerAction = %t, want %t", got, spec.worker)
			}
			if got := hasExternalEffect(Action{Kind: kind}); got != spec.externalEffect {
				t.Errorf("hasExternalEffect = %t, want %t", got, spec.externalEffect)
			}
		})
	}
}
