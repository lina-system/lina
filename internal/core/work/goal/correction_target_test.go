// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"reflect"
	"testing"
)

func TestCorrectionReconcilesLibraryVerificationTarget(t *testing.T) {
	for _, mode := range []string{"retire-latest", "retain-latest", "replace-all", "service"} {
		t.Run(mode, func(t *testing.T) {
			a, b := readyTask("a"), readyTask("b")
			g := oneGoal(a, b)
			if mode == "service" {
				g.ServiceTarget, g.NoServiceReason = "service", ""
			}
			e := deploymentEngine(t, g)
			for _, id := range []string{"a", "b"} {
				revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
				revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
				revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merge-" + id, Ref: "merge:" + id, Passed: true}})
				revisionReport(t, e, id, RunQA, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merge-" + id, Ref: "qa:" + id, Passed: true}})
			}
			before := revisionSnapshot(t, e).Goals[0]
			revisionEdit(t, e)
			next, want := []Task{a}, "merge-a"
			switch mode {
			case "retain-latest":
				next, want = []Task{a, b}, "merge-b"
			case "replace-all":
				a.Done = "replacement result"
				next, want = []Task{a}, ""
			case "service":
				want = "merge-b"
			}
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: next})
			got := revisionSnapshot(t, e).Goals[0]
			if got.TargetRevision != want {
				t.Fatalf("verification target=%q, want %q after %s", got.TargetRevision, want, mode)
			}
			if !reflect.DeepEqual(got.Evidence, before.Evidence) || !reflect.DeepEqual(got.MergedRevisions, before.MergedRevisions) {
				t.Fatal("correction rewrote observed merge history")
			}
			if mode != "replace-all" && !reflect.DeepEqual(got.Tasks[0], before.Tasks[0]) {
				t.Fatal("unchanged task lost its result")
			}
			// The corrected target must survive journal reload before derivation.
			cfg := e.cfg
			cfg.Epoch++
			var err error
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			kind, task := Verify, ""
			if mode == "replace-all" {
				kind, task = Assign, "a"
			} else if mode == "service" {
				kind = Deploy
			}
			action := revisionAction(t, e, task, kind)
			if kind != Assign && action.Revision != want {
				t.Fatalf("derived %s at %q, want %q", kind, action.Revision, want)
			}
		})
	}
}
