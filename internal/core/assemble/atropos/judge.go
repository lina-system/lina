// SPDX-License-Identifier: Apache-2.0
package atropos

import (
	"context"
	"math"
	"time"
)

// Judge is a non-chat adapter. A nil adapter leaves every choice to the main
// model. Implementations must honor cancellation and retain no late result.
type Judge interface {
	Decide(context.Context, []ClosedChoice) ([]Judgment, error)
}
type ClosedChoice struct {
	ID         string      `json:"id"`
	Revision   string      `json:"revision"`
	Input      string      `json:"input"`
	Candidates []Candidate `json:"candidates"`
}
type Candidate struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}
type Judgment struct {
	ID            string             `json:"id"`
	Revision      string             `json:"revision"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
}
type JudgeReport struct {
	Answers map[string]Judgment
	Latency time.Duration
}

// JudgeChoices pays one bounded call for all open choices. Invalid, stale,
// missing, tied or insufficiently confident answers independently hand off.
func JudgeChoices(ctx context.Context, judge Judge, choices []ClosedChoice, cap time.Duration, threshold float64) JudgeReport {
	report := JudgeReport{Answers: map[string]Judgment{}}
	if judge == nil || len(choices) == 0 || threshold <= 0 || threshold > 1 {
		return report
	}
	if cap <= 0 {
		cap = DefaultCap
	}
	bounded, cancel := context.WithTimeout(ctx, cap)
	defer cancel()
	start := time.Now()
	type result struct {
		answers []Judgment
		err     error
	}
	ready := make(chan result, 1)
	go func() { a, e := judge.Decide(bounded, choices); ready <- result{a, e} }()
	var out result
	select {
	case out = <-ready:
	case <-bounded.Done():
	}
	report.Latency = time.Since(start)
	if ctx.Err() != nil || bounded.Err() != nil || out.err != nil {
		return report
	}
	counts := map[string]int{}
	for _, a := range out.answers {
		counts[a.ID]++
	}
	for _, q := range choices {
		for _, a := range out.answers {
			if a.ID != q.ID || counts[a.ID] != 1 || a.Revision != q.Revision || a.Choice == "hold" || a.Choice == "not-applicable" || len(a.Probabilities) != len(q.Candidates) {
				continue
			}
			sum := 0.0
			valid := true
			selected := -1.0
			highest := -1.0
			unique := true
			for _, c := range q.Candidates {
				p, ok := a.Probabilities[c.ID]
				if !ok || math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
					valid = false
					break
				}
				sum += p
				if c.ID == a.Choice {
					selected = p
				}
				if p > highest {
					highest = p
					unique = true
				} else if p == highest {
					unique = false
				}
			}
			if valid && math.Abs(sum-1) < 0.000001 && unique && selected == highest && selected >= threshold {
				report.Answers[q.ID] = a
			}
		}
	}
	return report
}
