// SPDX-License-Identifier: Apache-2.0
package reactor

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

func liveFixture(t *testing.T, ids ...string) (*worker.Live, map[string]int) {
	t.Helper()
	live := worker.NewLive()
	ends := map[string]int{}
	for _, id := range ids {
		session := worker.Session{ID: id, Thread: worker.Thread{ID: "thread-" + id, TaskID: "task-" + id}}
		if err := live.Adopt(session, &idleSessionAdapter{events: make(chan worker.Event)}, func(context.Context) error {
			ends[id]++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return live, ends
}

func TestReapEndsOnlySessionsOutsideKeep(t *testing.T) {
	live, ends := liveFixture(t, "step-a", "step-b", "step-c")
	reaper := LiveSessions{Sessions: live}
	if err := reaper.Reap(t.Context(), map[string]bool{"step-b": true}); err != nil {
		t.Fatal(err)
	}
	if ends["step-a"] != 1 || ends["step-c"] != 1 || ends["step-b"] != 0 {
		t.Fatalf("reap ended the wrong sessions: %+v", ends)
	}
	if live.Count() != 1 {
		t.Fatalf("live count = %d", live.Count())
	}
}

func TestReapCurrentReadsTheKeepSet(t *testing.T) {
	live, ends := liveFixture(t, "step-a")
	reads := 0
	reaper := LiveSessions{Sessions: live, Keep: func(context.Context) (map[string]bool, error) {
		reads++
		return map[string]bool{}, nil
	}}
	if err := reaper.ReapCurrent(t.Context()); err != nil || reads != 1 || ends["step-a"] != 1 {
		t.Fatalf("current reap: %v reads=%d ends=%+v", err, reads, ends)
	}
}

// An unavailable keep source ends nothing: a failed read is not an empty set.
func TestReapCurrentEndsNothingWithoutKeep(t *testing.T) {
	live, ends := liveFixture(t, "step-a")
	reaper := LiveSessions{Sessions: live, Keep: func(context.Context) (map[string]bool, error) {
		return nil, errors.New("ledger unavailable")
	}}
	if err := reaper.ReapCurrent(t.Context()); err == nil {
		t.Fatal("missing keep set was treated as empty")
	}
	if ends["step-a"] != 0 || live.Count() != 1 {
		t.Fatalf("unavailable keep ended a session: %+v %d", ends, live.Count())
	}
}

// idleSessionAdapter has no process: the registry only reads its event stream.
type idleSessionAdapter struct{ events chan worker.Event }

func (*idleSessionAdapter) Name() string                            { return "idle" }
func (*idleSessionAdapter) Version(context.Context) (string, error) { return "idle-1", nil }
func (*idleSessionAdapter) Capabilities() worker.CapabilitySet      { return nil }
func (*idleSessionAdapter) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, errors.New("not startable")
}
func (*idleSessionAdapter) Input(context.Context, worker.Thread, worker.Input) error {
	return errors.New("not startable")
}
func (*idleSessionAdapter) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	return errors.New("not startable")
}
func (a *idleSessionAdapter) Events() <-chan worker.Event { return a.events }
func (*idleSessionAdapter) Answer(context.Context, worker.Thread, worker.Answer) error {
	return errors.New("not startable")
}
func (*idleSessionAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (*idleSessionAdapter) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{}, errors.New("not startable")
}
