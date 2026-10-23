// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"slices"
)

// Compare inputs, never execution claims. Dependency order is not an input.
func sameTaskDefinition(a, b Task) bool {
	ad, bd := slices.Clone(a.DependsOn), slices.Clone(b.DependsOn)
	slices.Sort(ad)
	slices.Sort(bd)
	return a.ID == b.ID && a.Done == b.Done && a.Repository == b.Repository &&
		a.Base == b.Base && a.Area == b.Area && a.Worker == b.Worker &&
		a.Resources == b.Resources && a.Repairs == b.Repairs && slices.Equal(ad, bd)
}

func correctedTaskChanges(old, next []Task) map[string]bool {
	changed := map[string]bool{}
	byID := map[string]Task{}
	for _, t := range next {
		byID[t.ID] = t
	}
	for _, t := range old {
		n, ok := byID[t.ID]
		if !ok || !sameTaskDefinition(t, n) {
			changed[t.ID] = true
		}
	}
	// Both graphs matter: removing a dependency changes the downstream input
	// too. A superseded repair result is also an input of its original task.
	all := append(slices.Clone(old), next...)
	for again := true; again; {
		again = false
		for _, t := range all {
			if changed[t.ID] {
				continue
			}
			invalidate := t.RepairedBy != "" && changed[t.RepairedBy]
			for _, dep := range t.DependsOn {
				invalidate = invalidate || changed[dep]
			}
			if invalidate {
				changed[t.ID], again = true, true
			}
		}
	}
	return changed
}
