// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
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
