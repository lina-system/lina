// SPDX-License-Identifier: Apache-2.0
package budget_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
)

func TestMeterCountsTripsAndUnionOfToolTime(t *testing.T) {
	now := time.Unix(100, 0)
	meter := budget.Meter{Now: func() time.Time { return now }}
	g := meter.Start("turn", now)
	now = now.Add(time.Hour) // model/preparation time is not tool time
	if got := g.Snapshot(); got.Level != budget.Under || got.ToolTime != 0 {
		t.Fatal(got)
	}
	endA := g.Tool()
	now = now.Add(10 * time.Second)
	endB := g.Tool()
	now = now.Add(10 * time.Second)
	endA()
	endA() // completion is idempotent
	now = now.Add(10 * time.Second)
	endB()
	g.Round()
	got := g.Snapshot()
	if got.Level != budget.Soft || got.ToolTime != 30*time.Second || got.Rounds != 1 || got.Waiting != time.Hour+30*time.Second {
		t.Fatal(got)
	}
	now = now.Add(time.Hour)
	if got := g.Snapshot(); got.ToolTime != 30*time.Second {
		t.Fatal(got)
	}
	end := g.Tool()
	now = now.Add(30 * time.Second)
	if got := g.Snapshot(); got.Level != budget.Hard {
		t.Fatal(got)
	}
	end()
}

func TestMeterRoundBoundariesAndContinuation(t *testing.T) {
	now := time.Unix(100, 0)
	meter := budget.Meter{Now: func() time.Time { return now }}
	g := meter.Start("turn", now)
	for i := 1; i <= 10; i++ {
		g.Round()
		want := budget.Under
		if i >= 10 {
			want = budget.Hard
		} else if i >= 6 {
			want = budget.Soft
		}
		if got := g.Snapshot(); got.Rounds != i || got.Level != want {
			t.Fatal(got)
		}
	}
	// A continuation carries the gauge; a fresh turn gets a new gauge.
	continued := g
	continued.Round()
	if continued.Snapshot().Rounds != 11 || meter.Start("next", now).Snapshot().Level != budget.Under {
		t.Fatal("budget reset or leaked between turns")
	}
}
