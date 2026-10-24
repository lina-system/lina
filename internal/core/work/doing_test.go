// SPDX-License-Identifier: Apache-2.0
package work

import (
	"github.com/thisisjun786/lina/internal/core/worker/doing"
	"testing"
	"time"
)

func TestDoingIsDurableFencedAndNotCompletion(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	startFixture(t, s)
	observed := doing.Snapshot{Line: "Login · Running tests", Busy: true, Since: time.Unix(1, 0).UTC()}
	if err := s.ReportDoing(t.Context(), "task-1", "run-1", 1, observed); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportDoing(t.Context(), "task-1", "run-1", 1, observed); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(t.Context(), "task-1")
	if err != nil || c.Doing == nil || *c.Doing != observed || c.Receipt != nil || !c.Active {
		t.Fatalf("observation changed work: %+v %v", c, err)
	}
	if err := s.ReportDoing(t.Context(), "task-1", "old", 2, observed); err == nil {
		t.Fatal("old run accepted")
	}
	observed.Busy = false
	observed.Since = time.Unix(2, 0).UTC()
	if err := s.ReportDoing(t.Context(), "task-1", "run-1", 2, observed); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get(t.Context(), "task-1")
	if !c.Active || c.State != "working" || c.Receipt != nil {
		t.Fatal("idle became done")
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportDoing(t.Context(), "task-1", "run-1", 2, observed); err == nil {
		t.Fatal("finished retry accepted")
	}
}
