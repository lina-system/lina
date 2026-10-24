// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/runs"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestGoalStepWaitReadsOwnedRecordAndExpiry(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	data := []byte(fmt.Sprintf("---\nid: \"target\"\nkind: memo\nstate: active\nperson_id: %q\nagent_id: %q\n---\n\n# Record\n", r.person, r.agent))
	revision, err := repo.Commit(t.Context(), records.Change{ID: "target", Path: "inbox/target.md", Data: data}, records.Actor{Person: r.person, Agent: r.agent})
	if err != nil {
		t.Fatal(err)
	}
	d := &codeGoalDriver{runtime: r}
	g := goal.Goal{Person: r.person}
	for _, tc := range []struct {
		name   string
		person string
		wait   goal.StepWait
		status goal.Outcome
	}{
		{"date", r.person, goal.StepWait{At: 1}, goal.OutcomeSucceeded},
		{"changed", r.person, goal.StepWait{RecordID: "target", Revision: "previous", Deadline: time.Now().Add(time.Hour).Unix()}, goal.OutcomeSucceeded},
		{"expired", r.person, goal.StepWait{RecordID: "target", Revision: revision, Deadline: 1}, goal.OutcomeNeedsPerson},
		{"foreign", "other", goal.StepWait{RecordID: "target", Revision: "previous", Deadline: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g.Person = tc.person
			o, err := d.waitStep(t.Context(), g, goal.Task{Wait: &tc.wait}, goal.Action{ID: "wait"})
			if tc.status == "" {
				if err == nil {
					t.Fatal("foreign record observed")
				}
				return
			}
			if err != nil || o.Status != tc.status || !o.EffectsSettled {
				t.Fatalf("observation=%+v error=%v", o, err)
			}
			if tc.status == goal.OutcomeSucceeded && (!o.Proof.Passed || o.Proof.Ref == "" || o.Proof.Revision == "") {
				t.Fatal("unproven close")
			}
		})
	}
}

func TestGoalStepBackgroundUsesRecordedOrigin(t *testing.T) {
	r, _, owner, source := managementFixture(t)
	d := &codeGoalDriver{runtime: r}
	g := goal.Goal{ID: "goal", Person: r.person, Agent: owner, Conversation: "manager:goal", Turn: "coding:source"}
	a := codingAssignment{ID: g.ID, Manager: "manager", Owner: owner, Conversation: g.Conversation, Origin: dispatch.Origin{Person: r.person, Agent: source, Conversation: "main:" + source, Turn: "source-turn"}}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO coding_managers(id,person,owner,unit,conversation) VALUES(?,?,?,?,?)", a.Manager, g.Person, g.Agent, g.ID, g.Conversation); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT INTO coding_assignments(id,manager,record) VALUES(?,?,?)", a.ID, a.Manager, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o, err := d.goalRunOrigin(t.Context(), g)
	if err != nil || o.Agent != source || o.Conversation != a.Origin.Conversation || o.Turn != a.Origin.Turn || o.Epoch != r.epoch {
		t.Fatalf("lost originating authority: %+v %v", o, err)
	}
	for _, change := range []func(*goal.Goal){func(g *goal.Goal) { g.Person = "other" }, func(g *goal.Goal) { g.Agent = source }, func(g *goal.Goal) { g.Conversation = "main:" + owner }} {
		other := g
		change(&other)
		if _, err = d.goalRunOrigin(t.Context(), other); err == nil {
			t.Fatal("mismatched delegation accepted")
		}
	}
}

// A start rejected before admission never created a run, so it must settle the
// action instead of holding its capacity forever. The same holds for recovery:
// no receipt means no effect can have started.
func TestGoalStepPartialFollowUpMarksTheQuestionOpen(t *testing.T) {
	r := controlRuntime(t)
	r.route = &modelpath.ResponsesConfig{}
	var calls atomic.Int32
	s, err := runs.New(runs.Config{Epoch: r.epoch, DB: r.db, Assets: r.assets, WorkDir: filepath.Join(r.root, "work"), Authorize: r.authorizeRun, Stream: func(_ context.Context, req provider.Request) (provider.Stream, error) {
		n := calls.Add(1)
		if n == 1 {
			return bootAnswer(runs.Artifact{Body: "Notes so far.", Done: false, Missing: []string{"the fee"}}), nil
		}
		sawMaterial := false
		for _, item := range req.Items {
			if strings.Contains(string(item), "Notes so far.") {
				sawMaterial = true
			}
		}
		if !sawMaterial {
			t.Errorf("follow-up did not receive the partial material")
		}
		var brief runs.Brief
		if len(req.Items) == 0 || json.Unmarshal(req.Items[0], &struct {
			Content *string `json:"content"`
		}{}) != nil {
			t.Errorf("follow-up request missing brief: %s", req.Items)
		}
		var message struct {
			Content string `json:"content"`
		}
		if len(req.Items) > 0 {
			_ = json.Unmarshal(req.Items[0], &message)
			_ = json.Unmarshal([]byte(message.Content), &brief)
		}
		if n == 2 && (brief.Scope != "the fee" || brief.FollowOf == "") {
			t.Errorf("follow-up was not scoped to the gap: %+v", brief)
		}
		return bootAnswer(runs.Artifact{Body: "Notes so far.", Done: false, Missing: []string{"the fee"}, OpenReason: "The sources do not state it."}), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r.background = &backgroundRuntime{service: s}
	d := &codeGoalDriver{runtime: r}
	g := goal.Goal{ID: "goal", Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn"}
	task := goal.Task{ID: "step", Kind: goal.BackgroundStep, Background: &goal.BackgroundBrief{Goal: "collect", Scope: "named sources", NotTouch: "files", Done: "report", Result: "document", Checks: []goal.BackgroundCheck{{Name: "report", Kind: "claimed"}}}}
	first, err := d.backgroundStep(t.Context(), g, task, goal.Action{ID: "start", Kind: goal.Assign, StepKind: goal.BackgroundStep}, false)
	if err != nil || first.Status != goal.OutcomePartial || first.Partial == nil || len(first.Partial.Missing) != 1 || first.Partial.Missing[0] != "the fee" {
		t.Fatalf("partial observation: %+v %v", first, err)
	}
	origin, err := d.goalRunOrigin(t.Context(), g)
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := s.Started(t.Context(), origin, "start")
	if err != nil || !found {
		t.Fatal(err, found)
	}
	follow, ok := runs.FollowUpBrief(record)
	if !ok {
		t.Fatal("no follow-up brief")
	}
	task.ResearchFollowUp = &goal.BackgroundBrief{Goal: follow.Goal, Scope: follow.Scope, NotTouch: follow.NotTouch, Done: follow.Done, Result: follow.Result, FollowOf: follow.FollowOf, Checks: []goal.BackgroundCheck{{Name: "report", Kind: "claimed"}}, Refs: []goal.BackgroundRef{{ID: follow.Refs[0].ID, Revision: follow.Refs[0].Revision}}}
	second, err := d.backgroundStep(t.Context(), g, task, goal.Action{ID: "follow", Kind: goal.Assign, StepKind: goal.BackgroundStep}, false)
	if err != nil || second.Status != goal.OutcomePartial || second.Partial == nil || len(second.Partial.Open) != 1 || second.Partial.Open[0].Question != "the fee" || second.Partial.Open[0].Reason == "" {
		t.Fatalf("open question: %+v %v", second, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("runs=%d", calls.Load())
	}
	task.ResearchGap = []string{"the fee"}
	task.Stage = goal.TaskDone
	task.Result = second.Proof
	task.Result.Passed = true
	proof, err := d.readStepResult(t.Context(), goal.State{Actions: []goal.Action{{ID: "follow", GoalID: g.ID, TaskID: task.ID, Kind: goal.Assign, State: "finished"}}}, g, task)
	if err != nil || !proof.Passed || proof.Revision != second.Proof.Revision {
		t.Fatalf("partial result was not readable: %+v %v", proof, err)
	}
	check := goal.CriterionCheck{Kind: goal.JudgmentEvidence, Method: "research.rubric", Target: "document", Rubric: "answered or marked open with a reason", TaskID: task.ID}
	state := goal.State{Actions: []goal.Action{{ID: "follow", GoalID: g.ID, TaskID: task.ID, Kind: goal.Assign, State: "finished", Attempt: 2}}}
	observed, _, err := d.researchCriterion(t.Context(), state, g, task, goal.Action{Revision: "1", Check: &check, GoalRevision: 1, CheckRound: 1})
	if err != nil || !observed.Proof.Passed {
		t.Fatalf("stored material did not satisfy the rubric: %+v %v", observed, err)
	}
	current, err := r.assets.Get(t.Context(), second.Partial.MaterialID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := r.assets.Read(t.Context(), current.ID, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	saved["Artifact"] = map[string]any{"body": "unrelated notes", "done": false}
	edited, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.assets.Edit(t.Context(), current.ID, current.Revision, bytes.NewReader(edited), assets.Actor{PersonID: r.person, AgentID: r.agent}); err != nil {
		t.Fatal(err)
	}
	observed, _, err = d.researchCriterion(t.Context(), state, g, task, goal.Action{Revision: "1", Check: &check, GoalRevision: 1, CheckRound: 1})
	if err != nil || observed.Proof.Passed {
		t.Fatalf("edited material still passed: %+v %v", observed, err)
	}
	if err = r.assets.Trash(t.Context(), current.ID, assets.Actor{PersonID: r.person, AgentID: r.agent}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.researchCriterion(t.Context(), state, g, task, goal.Action{Revision: "1", Check: &check, GoalRevision: 1, CheckRound: 1}); err == nil {
		t.Fatal("trashed material was still accepted")
	}
}

func TestAuthorizeRunAllowsOnlyThePersonsMaterial(t *testing.T) {
	r := controlRuntime(t)
	r.route = &modelpath.ResponsesConfig{}
	owned, err := r.assets.SaveRunResult(t.Context(), "0123456789abcdef0123456789abcdef", []byte(`{"body":"notes"}`), assets.Actor{PersonID: r.person, AgentID: r.agent})
	if err != nil {
		t.Fatal(err)
	}
	origin := runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn", Epoch: r.epoch}
	brief := runs.Brief{Goal: "collect", Scope: "named sources", NotTouch: "files", Done: "report", Result: "document", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}, Refs: []runs.Ref{{ID: owned.ID, Revision: owned.Revision}}}
	if err = r.authorizeRun(t.Context(), origin, brief); err != nil {
		t.Fatal(err)
	}
	foreign, err := r.assets.SaveRunResult(t.Context(), "abcdef0123456789abcdef0123456789", []byte(`{"body":"other"}`), assets.Actor{PersonID: "someone-else", AgentID: r.agent})
	if err != nil {
		t.Fatal(err)
	}
	brief.Refs = []runs.Ref{{ID: foreign.ID, Revision: foreign.Revision}}
	if err = r.authorizeRun(t.Context(), origin, brief); !errors.Is(err, runs.ErrScope) {
		t.Fatalf("foreign material: %v", err)
	}
}

func bootAnswer(a runs.Artifact) provider.Stream {
	b, _ := json.Marshal(a)
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(b)}}})
	return &bootStream{events: []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}
}

type bootStream struct{ events []provider.Event }

func (s *bootStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*bootStream) Meta() provider.Meta { return provider.Meta{} }
func (*bootStream) Close() error        { return nil }

func TestGoalStepBackgroundRefusalSettles(t *testing.T) {
	for _, observe := range []bool{false, true} {
		name := "execute"
		if observe {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			r := controlRuntime(t)
			s, err := runs.New(runs.Config{Epoch: r.epoch, DB: r.db, Assets: r.assets, WorkDir: filepath.Join(r.root, "work"), Authorize: func(context.Context, runs.Origin, runs.Brief) error { return runs.ErrScope }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				t.Error("refused background start executed")
				return nil, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			r.background = &backgroundRuntime{service: s}
			d := &codeGoalDriver{runtime: r}
			g := goal.Goal{ID: "goal", Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn"}
			task := goal.Task{ID: "step", Kind: goal.BackgroundStep, Background: &goal.BackgroundBrief{Goal: "collect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []goal.BackgroundCheck{{Name: "report", Kind: "claimed"}}}}
			o, err := d.backgroundStep(t.Context(), g, task, goal.Action{ID: "start", Kind: goal.Assign, StepKind: goal.BackgroundStep}, observe)
			if err != nil || o.Status != goal.OutcomeNeedsPerson || !o.EffectsSettled {
				t.Fatalf("refusal left an unsettled effect: %+v %v", o, err)
			}
			origin, err := d.goalRunOrigin(t.Context(), g)
			if err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.Started(t.Context(), origin, "start"); err != nil || found {
				t.Fatalf("refused start admitted a run: %v %v", found, err)
			}
		})
	}
}
