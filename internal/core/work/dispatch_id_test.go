// SPDX-License-Identifier: Apache-2.0
package work

import "testing"

// TestDispatchIDIsStable keeps the id of an admitted dispatch fixed: dispatch
// records and their task cards are already stored under it.
func TestDispatchIDIsStable(t *testing.T) {
	if got := DispatchID("turn", "call"); got != "e79a420b4ba06ccc688d56090ec7897f4cf9c8ae984b9129b0736b0aa3ec35b4" {
		t.Fatalf("dispatch id = %s", got)
	}
}
