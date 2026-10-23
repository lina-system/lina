// SPDX-License-Identifier: Apache-2.0
package work

import "testing"

func TestScheduledStopRetainsUnsettledReservations(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-start", true: "active-turn"}[started], func(t *testing.T) {
			s, _ := testStore(t)
			a := scheduled("a")
			a.PersonID = "person"
			if err := s.Schedule(t.Context(), []Task{a}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Release(t.Context(), available()); err != nil {
				t.Fatal(err)
			}
			var run Run
			ref := "a:0"
			if started {
				run = startScheduled(t, s, "a")
				ref = "a:1"
				if _, err := s.StopScheduled(t.Context(), "person", "conversation", "a:0"); err == nil {
					t.Fatal("stale row stopped a new execution")
				}
			}
			if n, err := s.StopScheduled(t.Context(), "person", "conversation", ref); err != nil || n != 1 {
				t.Fatalf("stop: %d %v", n, err)
			}
			if err := s.Recover(t.Context(), "restart"); err != nil {
				t.Fatal(err)
			}
			c, err := s.Get(t.Context(), "a")
			if err != nil || !c.Scheduling.Closed || !c.Scheduling.CapacityHeld || !c.Scheduling.AreaHeld || c.State != "unknown" {
				t.Fatalf("unsettled effect lost its fence: %+v %v", c, err)
			}
			if started {
				if err := s.Finish(t.Context(), "a", run.ID, "completed"); err != nil {
					t.Fatal(err)
				}
				c, _ = s.Get(t.Context(), "a")
				if c.State != "stopped" || c.Scheduling.AreaHeld || c.Scheduling.CapacityHeld {
					t.Fatalf("late completion reopened stopped work: %+v", c)
				}
			}
		})
	}
}
