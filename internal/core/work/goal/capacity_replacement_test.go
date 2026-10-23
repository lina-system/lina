// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func TestCapacityReplacementUsesObservedEffect(t *testing.T) {
	for _, resource := range []string{"cpu", "memory", "disk"} {
		t.Run(resource, func(t *testing.T) {
			n := readyNode("n")
			req := Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}
			switch resource {
			case "cpu":
				n.CPUCores, n.CPULimit, n.LoadCPU = 4, 4, 2
			case "memory":
				n.MemoryLimitMB, n.AvailableMemoryMB = 2000, 1000
			case "disk":
				n.DiskLimitMB, n.AvailableDiskMB = 2000, 1000
			}
			a := Action{ID: "replacement", ObserveActionID: "dispatched", NodeID: n.ID, Kind: Assign, Resources: req}
			for _, observed := range []string{"dispatched", "replacement"} {
				n.ObservedActions = []string{observed}
				if got := Slots(n, []Action{a}, req, time.Unix(100, 0)); got != 1 {
					t.Errorf("%s observed: slots=%d, want 1", observed, got)
				}
			}
			n.ObservedActions = []string{"unrelated"}
			if got := Slots(n, []Action{a}, req, time.Unix(100, 0)); got != 0 {
				t.Errorf("unobserved reservation: slots=%d, want 0", got)
			}
			n.ObservedActions = []string{"dispatched"}
			queued := Action{ID: "queued", NodeID: n.ID, Kind: Assign, Resources: req}
			if got := Slots(n, []Action{a, queued}, req, time.Unix(100, 0)); got != 0 {
				t.Errorf("resource cap exceeded: slots=%d, want 0", got)
			}
		})
	}
}
