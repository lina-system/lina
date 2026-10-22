// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"path"
	"strings"
)

type areaReservation struct {
	owner string
	task  Task
}

// Use the directory before the first glob component as a conservative lease.
// Distinct literal subtrees are provably disjoint; uncertain glob intersections
// serialize instead of allowing concurrent writes to a shared path.
func areaRoot(area string) string {
	if strings.HasPrefix(area, "/") || strings.ContainsAny(area, "\\\x00") {
		return ""
	}
	for _, part := range strings.Split(area, "/") {
		if part == ".." {
			return ""
		}
	}
	if i := strings.IndexAny(area, "*?[{"); i >= 0 {
		area = area[:strings.LastIndex(area[:i], "/")+1]
	}
	root := path.Clean(area)
	if root == "." {
		return ""
	}
	return root
}
func areasOverlap(a, b Task) bool {
	if a.Repository != b.Repository {
		return false
	}
	x, y := areaRoot(a.Area), areaRoot(b.Area)
	return x == "" || y == "" || x == y || strings.HasPrefix(x, y+"/") || strings.HasPrefix(y, x+"/")
}
