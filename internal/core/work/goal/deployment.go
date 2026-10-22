// SPDX-License-Identifier: Apache-2.0
package goal

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
