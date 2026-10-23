// SPDX-License-Identifier: Apache-2.0
package review

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
)

// Assess is Core's mechanical boundary, not a model finding classifier. The
// existing GitHub reader requires all observed checks and current-head review.
// An empty State means the manager may check the definition, not acceptance.
func Assess(p antigravity.PullRequest, repository, branch, base string, files, areas []string) Record {
	if err := p.Verify(repository, branch, base, p.Head); err != nil {
		return Record{State: "blocked", Cause: "merge_precondition", Evidence: err.Error()}
	}
	var misses []string
	for _, file := range files {
		allowed := false
		for _, area := range areas {
			allowed = allowed || work.AreaContains(area, file)
		}
		if !allowed {
			misses = append(misses, file)
		}
	}
	if len(misses) > 0 {
		raw, _ := json.Marshal(map[string]any{"scope_misses": misses})
		return Record{State: "complete", Verdict: "needs_changes", Evidence: string(raw)}
	}
	return Record{}
}
