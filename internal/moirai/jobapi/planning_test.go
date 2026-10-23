// SPDX-License-Identifier: Apache-2.0
package jobapi

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPlanDependencyTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		deps []string
		bad  bool
	}{
		{name: "none"},
		{name: "earlier todo", deps: []string{"earlier"}},
		{name: "later todo in another project", deps: []string{"later"}},
		{name: "parent project", deps: []string{"parent"}, bad: true},
		{name: "other project", deps: []string{"other"}, bad: true},
		{name: "missing", deps: []string{"missing"}, bad: true},
		{name: "self", deps: []string{"dependent"}, bad: true},
		{name: "duplicate", deps: []string{"earlier", "earlier"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft := PlanDraft{Title: "Guide", Summary: "Write and publish the guide", Entries: []PlanEntry{
				{ID: "parent", Kind: "project", Title: "Write", Done: "Guide written"},
				{ID: "earlier", Kind: "todo", Title: "Outline", Parent: "parent", Done: "Outline ready"},
				{ID: "dependent", Kind: "todo", Title: "Write guide", Parent: "parent", Done: "Draft ready", DependsOn: tc.deps},
				{ID: "other", Kind: "project", Title: "Research", Done: "Sources ready"},
				{ID: "later", Kind: "todo", Title: "Gather sources", Parent: "other", Done: "Sources collected"},
			}}
			raw, err := json.Marshal(draft)
			if err != nil {
				t.Fatal(err)
			}
			_, decodedErr := DecodePlan(raw, "goal")
			for name, err := range map[string]error{"validate": ValidatePlan("goal", draft), "decode": decodedErr} {
				if tc.bad && !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: invalid dependency targets %v: got %v, want ErrInvalid", name, tc.deps, err)
				} else if !tc.bad && err != nil {
					t.Errorf("%s: valid dependency targets %v: %v", name, tc.deps, err)
				}
			}
		})
	}
}
