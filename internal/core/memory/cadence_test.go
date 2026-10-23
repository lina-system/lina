// SPDX-License-Identifier: Apache-2.0
package memory

import "testing"

// Values exercised against the pinned reference's _effective_cadence. Both
// empty replies and ordinary failed cycles increment the same empty streak.
func TestBriefReferenceCadence(t *testing.T) {
	for _, v := range []struct{ base, streak, want int64 }{{1, 0, 1}, {1, 1, 2}, {1, 7, 8}, {1, 100, 8}, {2, 0, 2}, {2, 1, 3}, {2, 14, 16}, {3, 10, 13}, {3, 21, 24}, {3, 100, 24}} {
		if got := BriefCadence(v.base, v.streak); got != v.want {
			t.Fatalf("base=%d streak=%d got=%d want=%d", v.base, v.streak, got, v.want)
		}
	}
}
