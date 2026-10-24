// SPDX-License-Identifier: Apache-2.0
package recall_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
)

func TestSummaryRecallExcludesProtectedDescendantsBeforeLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope      memory.Scope
		projection bool
	}{
		{"external", memory.NeverLeaves, false}, {"replay", memory.Private, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := database(t)
			base := time.Unix(1700000000, 0)
			tree := compaction.New(db)
			publish := func(id, conversation, event, child, text string) {
				t.Helper()
				frontier, err := tree.Frontier(t.Context(), conversation)
				if err != nil {
					t.Fatal(err)
				}
				p := compaction.Proposal{ID: id, Expected: frontier, Cover: []compaction.Coverage{{EventID: event}}, Output: compaction.Output{Model: "fixture", PromptVersion: "1", BuiltAt: base.Add(time.Hour), Text: text}}
				if child != "" {
					p.Children = []string{child}
				}
				if _, err := tree.Publish(t.Context(), p); err != nil {
					t.Fatal(err)
				}
			}
			admitAt(t, db, "hidden", "protected", "owner", "agent", "aurora secret", base, "")
			remember(t, db, "agent", "", "aurora private fact", tc.scope, "hidden")
			publish("leaf", "protected", "hidden", "", "aurora secret")
			admitAt(t, db, "next", "protected", "owner", "agent", "next event", base.Add(time.Second), "")
			publish("parent", "protected", "next", "leaf", "aurora secret")
			admitAt(t, db, "last", "protected", "owner", "agent", "last event", base.Add(2*time.Second), "")
			publish("root", "protected", "last", "parent", "aurora secret")
			// Protected summaries rank newer, so filtering after LIMIT would lose
			// the public result when only one result is requested.
			admitAt(t, db, "open", "public", "owner", "agent", "aurora public", base.Add(-time.Second), "")
			publish("public-summary", "public", "open", "", "aurora public")
			drain(t, db)
			scope := recall.Scope{Person: "owner", Agent: "agent", Kinds: []recall.Kind{recall.Summary}}
			if ids := searchIDs(t, db, scope, "aurora", 0); len(ids) != 4 {
				t.Fatalf("unrestricted summaries: %v", ids)
			}
			scope.ExternalModel, scope.Projection = true, tc.projection
			for _, limit := range []int{0, 1} {
				results, err := recall.Search(t.Context(), db, nil, scope, recall.Query{Text: "aurora", Limit: limit})
				if err != nil || len(results) != 1 || results[0].ID != "public-summary" {
					t.Fatalf("protected descendant reached recall at limit %d: %+v %v", limit, results, err)
				}
			}
		})
	}
}
