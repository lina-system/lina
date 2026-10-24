// SPDX-License-Identifier: Apache-2.0
package atropos

import (
	"context"
	"errors"
	"testing"
	"time"
)

type judgeFunc func(context.Context, []ClosedChoice) ([]Judgment, error)

func (f judgeFunc) Decide(ctx context.Context, q []ClosedChoice) ([]Judgment, error) {
	return f(ctx, q)
}
func TestJudgeClosedChoices(t *testing.T) {
	q := ClosedChoice{ID: "input", Revision: "v1", Candidates: []Candidate{{ID: "a"}, {ID: "b"}, {ID: "hold"}, {ID: "not-applicable"}}}
	for _, kind := range []string{"valid", "stale", "low", "outside", "tie", "missing", "error"} {
		t.Run(kind, func(t *testing.T) {
			judge := judgeFunc(func(context.Context, []ClosedChoice) ([]Judgment, error) {
				a := Judgment{ID: q.ID, Revision: q.Revision, Choice: "a", Probabilities: map[string]float64{"a": .9, "b": .1, "hold": 0, "not-applicable": 0}}
				switch kind {
				case "stale":
					a.Revision = "old"
				case "low":
					a.Probabilities["a"] = .7
					a.Probabilities["b"] = .3
				case "outside":
					a.Choice = "foreign"
				case "tie":
					a.Probabilities["a"] = .5
					a.Probabilities["b"] = .5
				case "missing":
					delete(a.Probabilities, "hold")
				case "error":
					return nil, errors.New("fixture")
				}
				return []Judgment{a}, nil
			})
			report := JudgeChoices(t.Context(), judge, []ClosedChoice{q}, time.Second, .8)
			if (len(report.Answers) == 1) != (kind == "valid") {
				t.Fatalf("%+v", report)
			}
		})
	}
}
func TestJudgeCancellationHandsOff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	finished := make(chan struct{})
	judge := judgeFunc(func(ctx context.Context, _ []ClosedChoice) ([]Judgment, error) {
		defer close(finished)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	report := JudgeChoices(ctx, judge, []ClosedChoice{{ID: "input"}}, time.Second, .8)
	<-finished
	if len(report.Answers) != 0 {
		t.Fatal(report)
	}
}
