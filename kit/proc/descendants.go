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

// descendantIdentities returns the processes reachable from root through
// recorded parent identities, excluding root itself. The walk repeats until it
// finds nothing new, so it does not depend on the order of the scan or on the
// intermediate processes still being alive when the scan runs.
func descendantIdentities(root uint64, all []processIdentity) []processIdentity {
	if root == 0 {
		return nil
	}
	known := map[uint64]struct{}{root: {}}
	var descendants []processIdentity
	for {
		added := false
		for _, id := range all {
			if id.uniqueID == 0 {
				continue
			}
			if _, ok := known[id.uniqueID]; ok {
				continue
			}
			if _, ok := known[id.parentUniqueID]; !ok {
				continue
			}
			known[id.uniqueID] = struct{}{}
			descendants = append(descendants, id)
			added = true
		}
		if !added {
			return descendants
		}
	}
}
