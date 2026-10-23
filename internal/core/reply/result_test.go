// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/kit/result"
)

func TestTypedReplyShapesAndBudgets(t *testing.T) {
	table := json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`)
	shapes := reply.Shapes{Status: "verdict-first", Comparison: "verdict-then-card", Candidates: 1, ThirdParty: "recipient-only"}
	for _, tc := range []struct {
		name, comparison, typ, summary string
		body                           json.RawMessage
		kind                           result.Type
		budget                         reply.Budget
		inline                         bool
		breaches                       []string
		budgetBreach                   bool
	}{
		{name: "card", comparison: "verdict-then-card", kind: result.TableType, summary: "A fits.", body: table},
		{name: "table", comparison: "verdict-then-table", kind: result.TableType, summary: "A fits.", body: table, inline: true},
		{name: "person-budget", comparison: "verdict-then-table", kind: result.TableType, summary: "A fits.", body: table, budget: reply.Budget{Lines: 1, Chars: 30}},
		{name: "comparison-verdict", comparison: "verdict-then-card", kind: result.TableType, summary: "A fits.\nB costs more.", body: table, breaches: []string{"comparison"}},
		{name: "status", comparison: "verdict-then-card", kind: result.TableType, typ: "status", summary: "Ready.\nBlocked.\nNext.\nLater.", body: table, breaches: []string{"status"}, budgetBreach: true},
		{name: "candidates", comparison: "verdict-then-card", kind: result.TableType, typ: "candidates", summary: "Choose A.", body: table, breaches: []string{"candidates"}},
		{name: "third-party", comparison: "verdict-then-card", kind: result.DraftType, summary: "Draft ready.", body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Private note.","audience":"person"}],"from_summary":null,"recipient":"Client"}`), breaches: []string{"third_party"}},
		{name: "unknown-draft", comparison: "verdict-then-card", kind: result.DraftType, summary: "Draft ready.", body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Private note."}],"from_summary":null}`)},
		{name: "unmarked-content", comparison: "verdict-then-card", kind: result.DraftType, summary: "Draft ready.", body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Private note."}],"from_summary":null,"recipient":"Client"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := shapes
			method.Comparison = tc.comparison
			c := config()
			c.Shapes = &method
			if tc.budget.Lines > 0 {
				c.Budget = tc.budget
			}
			r := result.Result{Type: tc.kind, ReplyType: tc.typ, Summary: tc.summary, Body: tc.body}
			view, err := reply.ProjectResult(c, r)
			if err != nil {
				t.Fatal(err)
			}
			if view.Inline != tc.inline || !reflect.DeepEqual(view.Check.ShapeBreaches, tc.breaches) || view.Check.BudgetBreach != tc.budgetBreach {
				t.Fatalf("view=%+v", view)
			}
			if !view.Inline && strings.Contains(view.Text, "Private note") {
				t.Fatal("detail leaked into summary")
			}
			if string(r.Body) != string(tc.body) || r.Summary != tc.summary {
				t.Fatal("source mutated")
			}
		})
	}
}
