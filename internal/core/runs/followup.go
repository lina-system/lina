// SPDX-License-Identifier: Apache-2.0
package runs

import "strings"

// PartialGap is what a landed partial result says is still missing.
func PartialGap(r Record) []string {
	var gap []string
	for _, q := range r.Artifact.Missing {
		q = strings.TrimSpace(q)
		if q != "" {
			gap = append(gap, q)
		}
	}
	return gap
}

// FollowUpBrief is the one continuation scoped to that gap. It continues from
// the partial material and does not widen the original brief.
func FollowUpBrief(prior Record) (Brief, bool) {
	gap := PartialGap(prior)
	if prior.Outcome != Partial || prior.ID == "" || len(gap) == 0 || prior.Material.ID == "" || prior.Material.Revision < 1 {
		return Brief{}, false
	}
	b := prior.Brief
	b.Mock = nil
	b.Scope = strings.Join(gap, "\n")
	b.Done = "Answer what is missing, or mark it open with the reason the sources cannot answer it."
	b.FollowOf = prior.ID
	b.Refs = []Ref{{ID: prior.Material.ID, Revision: prior.Material.Revision}}
	return b, true
}
