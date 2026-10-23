// SPDX-License-Identifier: Apache-2.0
package planning

import (
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"testing"
)

func TestDraftRequiresCompleteOrderedStructure(t *testing.T) {
	good := jobapi.PlanDraft{Title: "Workshop", Summary: "Prepare the workshop", Entries: []jobapi.PlanEntry{{ID: "project", Kind: "project", Title: "Workshop", Done: "Workshop delivered"}, {ID: "todo", Kind: "todo", Title: "Book room", Parent: "project", Done: "Room reserved"}}}
	if err := Validate("goal", good); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*jobapi.PlanDraft){
		func(d *jobapi.PlanDraft) { d.Entries[0].Done = "" },
		func(d *jobapi.PlanDraft) { d.Entries[1].Parent = "missing" },
		func(d *jobapi.PlanDraft) { d.Entries[1].ID = "project" },
		func(d *jobapi.PlanDraft) {
			d.Entries[0].DependsOn = []string{"todo"}
			d.Entries[1].DependsOn = []string{"project"}
		},
		func(d *jobapi.PlanDraft) { d.Entries[1].Kind = "goal" },
	} {
		copy := good
		copy.Entries = append([]jobapi.PlanEntry(nil), good.Entries...)
		mutate(&copy)
		if Validate("goal", copy) == nil {
			t.Fatalf("accepted invalid draft: %+v", copy)
		}
	}
	if Validate("memo", good) == nil {
		t.Fatal("unaccepted root kind")
	}
}
