// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/persona"
)

func TestReplyMethodUsesPreparedRecordAndActiveAgentFills(t *testing.T) {
	record, err := persona.ParseWorkMethod([]byte(`{"schema":1,"reply":{"comparison":"verdict-then-table","candidates":3},"fill":["reply.candidates"]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	prepared := agents.Preparation{WorkMethod: record, WorkMethodRevision: persona.WorkMethodRevision(record)}
	filled := agents.State{WorkMethodFills: map[string]agents.WorkMethodFill{
		"reply.candidates": {Value: json.RawMessage(`2`), Source: "confirmed-person-event"},
	}}
	for _, tc := range []struct {
		name       string
		prepared   agents.Preparation
		state      agents.State
		candidates int
		comparison string
	}{
		{"filled-agent", prepared, filled, 2, "verdict-then-table"},
		{"other-agent", prepared, agents.State{}, 3, "verdict-then-table"},
		{"retired-fill", agents.Preparation{WorkMethod: `{"schema":1}`}, filled, 1, "verdict-then-card"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shapes, err := reply.ShapesFrom(agents.ProjectWorkMethod(tc.prepared, tc.state))
			if err != nil {
				t.Fatal(err)
			}
			if shapes.Candidates != tc.candidates || shapes.Comparison != tc.comparison || shapes.Status != "verdict-first" || shapes.ThirdParty != "recipient-only" {
				t.Fatalf("wrong effective reply shapes: %+v", shapes)
			}
			c := config()
			c.Budget = reply.Budget{Lines: 1, Chars: 20}
			c.Shapes = &shapes
			f := filter(t, c)
			view, err := f.Push("Done. This reply exceeds the person's small budget.")
			if err != nil || !view.Cut || !f.Check().BudgetBreach || f.Budget() != c.Budget {
				t.Fatalf("work method bypassed person budget: %+v %v", view, err)
			}
		})
	}
}
