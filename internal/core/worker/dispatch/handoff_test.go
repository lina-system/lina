// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"os/exec"
	"testing"
)

type quotaStopAdapter struct {
	worker.Adapter
	events chan worker.Event
}

func (a *quotaStopAdapter) Name() string                                       { return "first" }
func (a *quotaStopAdapter) Version(context.Context) (string, error)            { return "fixture", nil }
func (a *quotaStopAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (a *quotaStopAdapter) Events() <-chan worker.Event                        { return a.events }
func (a *quotaStopAdapter) Start(_ context.Context, _ lease.Lease, s worker.StartSpec) (worker.Thread, error) {
	thread := worker.Thread{ID: "thread", TurnID: "turn", TaskID: s.TaskID, AttemptID: s.AttemptID, BindingEpoch: 1, ProcessGeneration: 1}
	a.events <- worker.Event{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.QuotaFailure, Code: "quota"}}
	return thread, nil
}

func TestQuotaHandoffWaitSurvivesCompletionOutbox(t *testing.T) {
	db := store(t)
	d := definition(t)
	gitFixture(t, d.Repository, "remote", "add", "origin", "https://github.com/fixture/project.git")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	area, err := work.OpenArea(root, git)
	if err != nil {
		t.Fatal(err)
	}
	defer area.Close(context.Background())
	s, err := openService(t, Config{DB: db, Work: work.New(db), Area: area, Root: root, Main: "main", Epoch: 1, Workers: []Factory{{Name: "first", Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return &quotaStopAdapter{events: make(chan worker.Event, 1)}, func(context.Context) error { return nil }, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	r := Record{ID: "task", Origin: Origin{Person: "person", Agent: "agent", Conversation: "conversation", Turn: "turn"}, Definition: d, Branch: "branch-task", State: "running", AreaHeld: true}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", r.ID, r.Origin.Turn, d.Repository, r.State, mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.execute(s.ctx, r)
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 || records[0].State != "queued" || records[0].Wait != "worker_quota" || len(records[0].Tried) != 1 || records[0].Packet == "" {
		t.Fatalf("settled first attempt stranded handoff: %+v %v", records, err)
	}
	card, err := s.cfg.Work.Get(t.Context(), r.ID)
	if err != nil || card.Active || card.Receipt != nil || card.Run.Generation != 1 {
		t.Fatalf("quota wait invented delivery: %+v %v", card, err)
	}
}
