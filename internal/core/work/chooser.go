// SPDX-License-Identifier: Apache-2.0
package work

import (
	"github.com/thisisjun786/lina/kit/provider"
	"slices"
	"time"
)

type WorkerCandidate struct {
	Name  string
	Slots int
	Quota []provider.QuotaRow
}

// ChoiceRule is provisional, ordered configuration. Tags must all match; quota
// and capacity always take precedence over a preferred product.
type ChoiceRule struct {
	Worker   string   `json:"worker"`
	Tags     []string `json:"tags,omitempty"`
	Priority int      `json:"priority"`
}

func ChooseWorker(candidates []WorkerCandidate, rules []ChoiceRule, tags, tried []string, now time.Time) string {
	best, bestQuota, bestPriority := "", -1, 0
	for _, c := range candidates {
		if c.Slots <= 0 || slices.Contains(tried, c.Name) {
			continue
		}
		rank := 2
		if len(c.Quota) == 0 {
			rank = 0
		}
		for _, row := range c.Quota {
			switch row.State {
			case provider.QuotaBlocked:
				if row.ResetsAt == nil || now.Before(*row.ResetsAt) {
					rank = -1
				} else if rank >= 0 {
					rank = 0
				}
			case provider.QuotaUnknown:
				if rank >= 0 {
					rank = 0
				}
			case provider.QuotaWarning:
				if rank > 1 {
					rank = 1
				}
			case provider.QuotaOK:
			default:
				if rank >= 0 {
					rank = 0
				}
			}
			if rank < 0 {
				break
			}
		}
		if rank < 0 {
			continue
		}
		priority := 0
		for _, rule := range rules {
			if rule.Worker != c.Name {
				continue
			}
			matches := true
			for _, tag := range rule.Tags {
				if !slices.Contains(tags, tag) {
					matches = false
				}
			}
			if matches {
				priority += rule.Priority
			}
		}
		if best == "" || rank > bestQuota || rank == bestQuota && priority > bestPriority {
			best, bestQuota, bestPriority = c.Name, rank, priority
		}
	}
	return best
}
