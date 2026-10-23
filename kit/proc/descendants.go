// SPDX-License-Identifier: Apache-2.0

package proc

// processIdentity is the kernel's immutable identity for one process: its own
// unique id and the unique id of the process that forked it. XNU records the
// parent id at fork and never rewrites it, so the link survives the parent's
// exit, orphan reparenting, a new session or process group, and a cleared
// environment.
type processIdentity struct {
	pid            int
	uniqueID       uint64
	parentUniqueID uint64
}

// trackDescendants adds every process reachable from tracked through recorded
// parent identities, and returns the ones it added. tracked carries identities
// forward between calls, so a child stays attributable after the parent that
// recorded it has been reaped. A zero identity is unreadable and never links.
func trackDescendants(tracked map[uint64]struct{}, all []processIdentity) []processIdentity {
	var added []processIdentity
	for {
		grew := false
		for _, id := range all {
			if id.uniqueID == 0 {
				continue
			}
			if _, ok := tracked[id.uniqueID]; ok {
				continue
			}
			if _, ok := tracked[id.parentUniqueID]; !ok {
				continue
			}
			tracked[id.uniqueID] = struct{}{}
			added = append(added, id)
			grew = true
		}
		if !grew {
			return added
		}
	}
}
