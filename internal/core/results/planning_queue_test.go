// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/records"
	reader "github.com/thisisjun786/lina/internal/moirai/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
	"time"
)

func TestObsoletePlanningTasksRetireAndReleaseFollowingWork(t *testing.T) {
	t.Parallel()
	s, db, cfg, _ := setup(t)
	repo, err := records.Open(t.TempDir(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	plans := NewPlanning(s, repo)
	root, err := plans.Accept(t.Context(), scope, "project", "Report", "Report delivered")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"title":"Report plan","summary":"Draft the report","entries":[{"id":"write","kind":"todo","title":"Write report","parent":"","done":"Draft exists"}]}`)
	input, err := plans.Input(t.Context(), scope, root.ID, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var card result.Card
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		card, err = plans.DraftTx(t.Context(), tx, scope, input, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	input.ResultID, input.BaseRevision = card.ID, 1
	clock := int64(1)
	open := func() *jobs.Service {
		service, err := jobs.New(jobs.Config{Store: db, Epoch: 1, Now: func() time.Time { return time.Unix(clock, 0) }, Definitions: map[string]jobs.Definition{"planning": {Agent: scope.AgentID, Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
			var in jobapi.PlanInput
			if err := json.Unmarshal(task.Input, &in); err != nil {
				return err
			}
			return plans.ApplyDraftTx(ctx, tx, scope, "planning", task.Sequence, in, output)
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := open()
	if err = service.Register(t.Context(), jobapi.Job{ID: "planning", Kind: jobapi.Planning, Enabled: true, Deterministic: true, Every: time.Second, Timeout: time.Second, Adoption: "plan-draft-v1"}); err != nil {
		t.Fatal(err)
	}
	enqueue := func(sequence int64, in jobapi.PlanInput) {
		t.Helper()
		encoded, _ := json.Marshal(in)
		if err := service.Enqueue(t.Context(), "planning", jobapi.Task{Sequence: sequence, Input: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(sequence int64) {
		t.Helper()
		clock += 2
		got, err := service.Apply(t.Context(), jobapi.Proposal{Epoch: 1, JobID: "planning", From: sequence - 1, Through: sequence, Output: raw})
		if err != nil || got.Watermark != sequence {
			t.Fatal(got, err)
		}
	}
	enqueue(1, input)
	enqueue(2, input)
	apply(1)
	apply(2)
	apply(2)
	if count(t, db, "plan_rejections") != 1 {
		t.Fatal("rejection was lost or duplicated")
	}
	current, err := s.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil || current.Revision != 2 {
		t.Fatal("rejection modified preview", current, err)
	}
	// Rebuild Core's job service and use the actual read-only worker reader.
	service = open()
	input.BaseRevision = 2
	enqueue(3, input)
	r, err := reader.Open(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	due, err := r.Ready(t.Context(), time.Unix(100, 0))
	if err != nil || len(due) != 1 || due[0].Task.Sequence != 3 {
		t.Fatal("queue stayed behind rejected work", due, err)
	}
	apply(3)
	input.BaseRevision = 3
	enqueue(4, input)
	if _, err = plans.Confirm(t.Context(), scope, card.ID, 3); err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head(t.Context())
	apply(4)
	if count(t, db, "plan_rejections") != 2 {
		t.Fatal("confirmation race did not retire replacement")
	}
	current, err = s.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil || current.Revision != 3 {
		t.Fatal(current, err)
	}
	if after, _ := repo.Head(t.Context()); after != head {
		t.Fatal("rejected replacement changed confirmed records")
	}
	input.ResultID, input.BaseRevision = "", 0
	enqueue(5, input)
	due, err = r.Ready(t.Context(), time.Unix(100, 0))
	if err != nil || len(due) != 1 || due[0].Task.Sequence != 5 {
		t.Fatal(due, err)
	}
	apply(5)
}
