// SPDX-License-Identifier: Apache-2.0
package review

import "strings"

// ReturnPayload is a missed work definition, not a GitHub review fix round.
// Lower priority findings remain in the original review record.
type ReturnPayload struct {
	Evidence string    `json:"evidence"`
	Findings []Finding `json:"findings,omitempty"`
}

func Return(r Record) (ReturnPayload, error) {
	if r.State != "complete" || r.Verdict != "needs_changes" || strings.TrimSpace(r.Evidence) == "" {
		return ReturnPayload{}, ErrState
	}
	p := ReturnPayload{Evidence: r.Evidence}
	for _, f := range r.Findings {
		if f.Severity == "P0" || f.Severity == "P1" {
			p.Findings = append(p.Findings, f)
		}
	}
	return p, nil
}

// Reason preserves finding identities in the close explanation.
func (p ReturnPayload) Reason() string {
	text := p.Evidence
	for _, line := range ReasonLines(p.Findings) {
		text += "\n" + line
	}
	return text
}

// ReasonLines renders findings as saved reason lines, keeping their ids so a
// return and a close name the same evidence.
func ReasonLines(findings []Finding) []string {
	var lines []string
	for _, f := range findings {
		lines = append(lines, f.Severity+" "+f.ID+" ("+f.File+"): "+f.Claim)
	}
	return lines
}
