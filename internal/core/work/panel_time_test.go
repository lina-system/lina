// SPDX-License-Identifier: Apache-2.0
package work

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWorkPanelRecordsStartAcrossRebuildAndRetry(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	before := time.Now().UTC()
	startFixture(t, s)
	card, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(card)
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	text, _ := fields["started_at"].(string)
	started, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || started.Before(before) || started.After(time.Now()) {
		t.Fatalf("missing recorded start: %s", raw)
	}
	if err = s.Create(t.Context(), taskFixture()); err != nil {
		t.Fatal("retry", err)
	}
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := json.Marshal(again)
	if string(raw) != string(other) {
		t.Fatalf("rebuild/retry changed time: %s -> %s", raw, other)
	}
}

func TestWorkPanelActivityAndLegacyClock(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	startFixture(t, s)
	for i, activity := range []string{"executing", "editing", "responding"} {
		if err := s.ReportActivity(t.Context(), "task-1", "run-1", uint64(i+1), activity); err != nil {
			t.Fatal(err)
		}
		if err := s.ReportActivity(t.Context(), "task-1", "run-1", uint64(i+1), activity); err != nil {
			t.Fatal("retry", err)
		}
		if err := s.Rebuild(t.Context()); err != nil {
			t.Fatal(err)
		}
		c, err := s.Get(t.Context(), "task-1")
		if err != nil || c.Activity != activity {
			t.Fatalf("lost activity: %+v %v", c, err)
		}
	}
	if err := s.ReportActivity(t.Context(), "task-1", "run-1", 4, "private command"); err == nil {
		t.Fatal("accepted arbitrary activity text")
	}
	events, err := s.Ledger(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].RecordedAt = ""
	}
	old, err := Project(events)
	if err != nil || !old.StartedAt.IsZero() {
		t.Fatalf("invented legacy time: %+v %v", old, err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportActivity(t.Context(), "task-1", "run-1", 4, "working"); err == nil {
		t.Fatal("stale activity accepted")
	}
}
