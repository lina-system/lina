// SPDX-License-Identifier: Apache-2.0
package work

import (
	"reflect"
	"testing"
)

func TestWorkerProgressIsDurableAndFencedByActiveRun(t *testing.T) {
	s, _ := testStore(t)
	startFixture(t, s)
	if err := s.ReportProgress(t.Context(), "task-1", "run-1", 1, "Writing result"); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		c, err := s.Get(t.Context(), "task-1")
		if err != nil || !c.Active || c.Receipt != nil || c.TurnStatus != "" || c.Stages[0] != (Stage{Name: "worker", Source: "worker_report", Summary: "Writing result"}) {
			t.Fatalf("progress became completion or disappeared: %+v %v", c, err)
		}
	}
	check()
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	check()
	before, err := s.Ledger(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReportProgress(t.Context(), "task-1", "other-run", 2, "stale"); err == nil {
		t.Fatal("foreign run updated progress")
	}
	if err = s.ReportProgress(t.Context(), "task-1", "run-1", 1, "Writing result"); err != nil {
		t.Fatal("identical observation not idempotent", err)
	}
	after, err := s.Ledger(t.Context(), "task-1")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retry or refusal changed ledger", err)
	}
	if err = s.Finish(t.Context(), "task-1", "run-1", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err = s.ReportProgress(t.Context(), "task-1", "run-1", 1, "Writing result"); err == nil {
		t.Fatal("finished run acknowledged a stale retry")
	}
	if err = s.ReportProgress(t.Context(), "task-1", "run-1", 2, "late"); err == nil {
		t.Fatal("finished run updated progress")
	}
}
