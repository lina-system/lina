// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

func newBootWork(t *testing.T) *work.Service {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return work.New(db)
}

func delegatedGraphTask(id, area string) work.Task {
	return work.Task{ID: id, AgentID: "agent", ConversationID: "conversation", Title: id, Done: "checked", Schedule: &work.Schedule{Group: "g", Repository: "owner/repo", Worker: "codex", Host: "local", Areas: []string{area}}}
}

func TestQuotaFailureIsRecordedBeforeTheNextRelease(t *testing.T) {
	for _, failure := range []worker.FailureKind{worker.QuotaFailure, worker.RateLimitFailure} {
		t.Run(string(failure), func(t *testing.T) {
			service := newBootWork(t)
			starts := map[string]int{}
			changed := make(chan struct{}, 4)
			var d *delegationRuntime
			reaction := reactor.NewScheduler(service, reactor.Hooks{
				Availability: func(context.Context) (work.Availability, error) {
					blocked, _ := d.reconcileQuotaStop(nil, time.Now())
					return work.Availability{Hosts: map[string]int{"local": 1}, BlockedWorkers: map[string]bool{"codex": blocked}}, nil
				},
				Start: func(_ context.Context, c work.Card) (work.Run, error) {
					starts[c.Task.ID]++
					return work.Run{ID: "run:" + c.Task.ID, TaskID: c.Task.ID, Generation: 1, Worker: "codex", NodeID: "local", ThreadID: "thread:" + c.Task.ID, TurnID: "turn"}, nil
				},
				Changed: func(context.Context) { changed <- struct{}{} },
			})
			d = &delegationRuntime{root: &conversationRuntime{work: service}, reactor: reaction, adapter: &codex.Session{}, done: make(chan struct{}), workerEvents: make(chan worker.Event, 1), leases: map[string]lease.Lease{}}
			if err := reaction.Schedule(t.Context(), []work.Task{delegatedGraphTask("a", "a/**"), delegatedGraphTask("b", "b/**")}); err != nil {
				t.Fatal(err)
			}
			<-changed
			ctx, cancel := context.WithCancel(t.Context())
			go d.events(ctx)
			t.Cleanup(func() { cancel(); <-d.done })
			d.workerEvents <- worker.Event{Thread: worker.Thread{ID: "thread:a", TurnID: "turn", TaskID: "a"}, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: failure, Code: "limit"}}
			select {
			case <-changed:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			if starts["a"] != 1 || starts["b"] != 0 {
				t.Fatalf("%s released the next task: %v", failure, starts)
			}
			waiting, err := service.Get(t.Context(), "b")
			if err != nil || waiting.WaitReason == nil || waiting.WaitReason.Kind != "quota" || waiting.Scheduling.Reserved {
				t.Fatalf("%s wait: %+v %v", failure, waiting.WaitReason, err)
			}
		})
	}
}

func TestQuotaStopWaitsForALaterMeasurement(t *testing.T) {
	now := time.Now()
	healthy := 10.0
	d := &delegationRuntime{}
	d.noteQuotaFailure(now)
	missed := []provider.QuotaRow{{Provider: provider.ChatGPT, Bucket: "primary", UsedPct: &healthy, Observed: now.Add(time.Second), State: provider.QuotaOK}}
	if blocked, _ := d.reconcileQuotaStop(missed, now.Add(time.Second)); !blocked {
		t.Fatal("snapshot that missed the failure cleared the stop")
	}
	if blocked, _ := d.reconcileQuotaStop(missed, now.Add(2*time.Second)); !blocked {
		t.Fatal("repeat of that snapshot cleared the stop")
	}
	stale := []provider.QuotaRow{{Provider: provider.ChatGPT, Bucket: "primary", UsedPct: &healthy, Observed: now.Add(-time.Hour), State: provider.QuotaOK}}
	if blocked, _ := d.reconcileQuotaStop(stale, now.Add(2*time.Second)); !blocked {
		t.Fatal("older measurement cleared the stop")
	}
	unknown := []provider.QuotaRow{{Provider: provider.ChatGPT, Bucket: "primary", Observed: now.Add(time.Minute), State: provider.QuotaUnknown}}
	if blocked, _ := d.reconcileQuotaStop(unknown, now.Add(time.Minute)); !blocked {
		t.Fatal("unknown snapshot cleared the stop")
	}
	later := []provider.QuotaRow{{Provider: provider.ChatGPT, Bucket: "primary", UsedPct: &healthy, Observed: now.Add(2 * time.Minute), State: provider.QuotaOK}}
	if blocked, _ := d.reconcileQuotaStop(later, now.Add(2*time.Minute)); blocked {
		t.Fatal("later measurement stayed blocked")
	}

	resetAt := now.Add(time.Hour)
	exhausted := 100.0
	limited := &delegationRuntime{}
	limited.noteQuotaFailure(now)
	rows := []provider.QuotaRow{{Provider: provider.ChatGPT, Bucket: "secondary", UsedPct: &exhausted, ResetsAt: &resetAt, Observed: now.Add(-time.Minute)}}
	blocked, reset := limited.reconcileQuotaStop(rows, now)
	if !blocked || !reset.Equal(resetAt) {
		t.Fatalf("exhausted window: blocked=%v reset=%v", blocked, reset)
	}
	if blocked, _ := limited.reconcileQuotaStop(rows, resetAt.Add(time.Second)); blocked {
		t.Fatal("elapsed reset stayed blocked")
	}
}

func TestQuotaWindowsStayIndependentAcrossSessions(t *testing.T) {
	now := time.Now()
	healthy := 10.0
	exhausted := 100.0
	weeklyReset := now.Add(24 * time.Hour)
	sessionA := []provider.QuotaRow{
		{Provider: provider.ChatGPT, Account: "acct", LimitID: "limit", Bucket: "primary", Window: provider.FiveHour, UsedPct: &healthy, Observed: now},
		{Provider: provider.ChatGPT, Account: "acct", LimitID: "limit", Bucket: "secondary", Window: provider.Weekly, UsedPct: &exhausted, ResetsAt: &weeklyReset, Observed: now},
	}
	sessionB := []provider.QuotaRow{
		{Provider: provider.ChatGPT, Account: "acct", LimitID: "limit", Bucket: "primary", Window: provider.FiveHour, UsedPct: &healthy, Observed: now.Add(time.Minute)},
		{Provider: provider.ChatGPT, Account: "acct", LimitID: "limit", Bucket: "secondary", Window: provider.Weekly, UsedPct: &healthy, Observed: now.Add(-time.Hour)},
	}
	merged := mergeQuotaWindows(sessionA, sessionB)
	blocked, reset := quotaRowsBlocked(merged, now)
	if !blocked || !reset.Equal(weeklyReset) {
		t.Fatalf("newer primary hid the exhausted weekly window: blocked=%v reset=%v rows=%+v", blocked, reset, merged)
	}
	var primary, weekly provider.QuotaRow
	for _, row := range merged {
		switch row.Bucket {
		case "primary":
			primary = row
		case "secondary":
			weekly = row
		}
	}
	if primary.Observed.Before(sessionB[0].Observed) || weekly.UsedPct == nil || *weekly.UsedPct < 100 {
		t.Fatalf("merge picked a whole snapshot: primary=%+v weekly=%+v", primary, weekly)
	}
}
