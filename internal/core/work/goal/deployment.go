// SPDX-License-Identifier: Apache-2.0
package goal

import "sort"

// A single repository is unambiguous, including for definitions saved before
// service_repository existed. Never infer a service from task or merge order.
func serviceRepository(g Goal) string {
	if g.ServiceRepository == "" && g.ServiceTarget != "" && len(g.Scope.Repositories) == 1 {
		return g.Scope.Repositories[0]
	}
	return g.ServiceRepository
}

func validServiceRepository(g Goal) bool {
	repo := serviceRepository(g)
	if repo == "" {
		return g.ServiceTarget == ""
	}
	return has(g.Scope.Repositories, repo)
}

// The old journal stored only the last merge globally. Recover only revisions
// established by its task receipts; task order cannot establish merge order.
func restoreMergedRevisions(g *Goal) {
	if len(g.MergedRevisions) != 0 || g.TargetRevision == "" {
		return
	}
	for _, task := range g.Tasks {
		if task.Merge.proves(g.TargetRevision) {
			if g.MergedRevisions == nil {
				g.MergedRevisions = map[string]string{}
			}
			g.MergedRevisions[task.Repository] = g.TargetRevision
		}
	}
}

func targetRevision(g Goal) string {
	// A graph made entirely of runs and waits has no Git merge. Its observed
	// results form a stable revision for runtime and criterion verification.
	if len(g.Tasks) > 0 && g.ServiceTarget == "" {
		onlyResults := true
		for _, t := range g.Tasks {
			if stepKind(t.Kind) == TaskStep {
				onlyResults = false
				break
			}
		}
		if onlyResults {
			var results [][2]string
			for _, t := range g.Tasks {
				if !stepLanded(t) {
					return ""
				}
				results = append(results, [2]string{t.ID, t.Result.Revision})
			}
			sort.Slice(results, func(i, j int) bool { return results[i][0] < results[j][0] })
			return actionID(Action{GoalID: g.ID, Revision: string(encode(results))})
		}
	}
	if g.ServiceTarget == "" {
		return g.TargetRevision
	}
	repo := serviceRepository(g)
	if revision := g.MergedRevisions[repo]; revision != "" {
		return revision
	}
	// Legacy single-repository records have no revision map. Use their target
	// only with a matching merge receipt from the service's repository.
	if len(g.MergedRevisions) == 0 && len(g.Scope.Repositories) == 1 {
		for _, task := range g.Tasks {
			if task.Repository == repo && task.Merge.proves(g.TargetRevision) {
				return g.TargetRevision
			}
		}
	}
	return ""
}
