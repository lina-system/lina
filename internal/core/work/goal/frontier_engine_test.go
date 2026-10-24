// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestFrontierMixedGraphCommitsClaimsAndRestart(t *testing.T) {
	t.Parallel()
	e, db := graphFixture(t)
	steps := []Step{
		{ID: "bg-entry", Name: "Read catalog", Kind: BackgroundStep, Repository: "repo", Done: "bg-entry", Resources: Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}, Background: &BackgroundBrief{Goal: "Read", Scope: "catalog", NotTouch: "files", Done: "bg-entry", Result: "notes", Checks: []BackgroundCheck{{Name: "bg-entry", Kind: "claimed"}}}},
		{ID: "time-entry", Name: "Wait for date", Kind: WaitStep, Repository: "repo", Done: "date reached", Wait: &StepWait{At: 200}},
		graphStep("build", "Prepare output"),
	}
	steps[2].DependsOn = []string{"bg-entry", "time-entry"}
	graph, err := submitGraph(t, e, 0, steps)
	if err != nil {
		t.Fatal(err)
	}
	n := readyNode("n")
	n.Workers["background"] = true
	if err = e.NodeChanged(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := revisionSnapshot(t, e)
	if len(s.Actions) != 2 {
		t.Fatalf("not all independent steps reserved: %+v", s.Actions)
	}
	claims := map[string]Action{}
	for _, a := range s.Actions {
		claimed, err := e.Claim(t.Context(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		claims[a.TaskID] = claimed
	}
	if reserves(claims["time-entry"]) || !reserves(claims["bg-entry"]) {
		t.Fatal("wrong capacity ownership")
	}
	if err = e.Report(t.Context(), claims["bg-entry"].ID, 1, Observation{Status: OutcomeSucceeded, EffectsSettled: true}); err == nil {
		t.Fatal("missing landing proof accepted")
	}
	if err = e.Report(t.Context(), claims["bg-entry"].ID, 1, Observation{Status: OutcomeSucceeded, EffectsSettled: true, Proof: Proof{Revision: "material:1", Ref: "material", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revisionSnapshot(t, e).Actions) != 2 {
		t.Fatal("dependent escaped open date wait")
	}
	e, err = NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: func() time.Time { return time.Unix(200, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s = revisionSnapshot(t, e)
	if !reflect.DeepEqual(*s.Goals[0].StepGraph, graph) {
		t.Fatal("restart rewrote graph")
	}
	if err = e.Report(t.Context(), claims["time-entry"].ID, 1, Observation{Status: OutcomeSucceeded, EffectsSettled: true, Proof: Proof{Revision: "200", Ref: "time-entry", Passed: true}}); err == nil {
		t.Fatal("old epoch accepted")
	}
	if err = e.Report(t.Context(), claims["time-entry"].ID, 2, Observation{Status: OutcomeSucceeded, EffectsSettled: true, Proof: Proof{Revision: "200", Ref: "time-entry", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s = revisionSnapshot(t, e)
	if len(s.Actions) != 3 {
		t.Fatalf("dependent not released after final blocker: %+v", s.Actions)
	}
	for _, a := range s.Actions {
		if a.TaskID == "build" && (a.Kind != Assign || a.State != "queued") {
			t.Fatalf("bad dependent: %+v", a)
		}
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(revisionSnapshot(t, e).Actions) != 3 {
		t.Fatal("duplicate derivation duplicated effect")
	}
}

func TestFrontierKindAdmissionRefusesMalformedWork(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind StepKind
		wait *StepWait
	}{
		{"unknown", "other", nil}, {"missing wait", WaitStep, nil}, {"ambiguous wait", WaitStep, &StepWait{At: 1, RecordID: "record", Revision: "old", Deadline: 2}}, {"missing deadline", WaitStep, &StepWait{RecordID: "record", Revision: "old"}}, {"missing brief", BackgroundStep, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := graphFixture(t)
			s := graphStep("step", "Prepare output")
			s.Kind = tc.kind
			s.Wait = tc.wait
			if tc.kind == WaitStep {
				s.Area, s.Base, s.Worker = "", "", ""
				s.Resources = Resources{}
			}
			if _, err := submitGraph(t, e, 0, []Step{s}); !errors.Is(err, GraphInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
