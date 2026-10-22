// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type parkedWorkDriver struct{}

func (parkedWorkDriver) Authorize(context.Context, work.Action) error { return work.ErrNeedsPerson }
func (parkedWorkDriver) Execute(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, work.ErrInvalid
}
func (parkedWorkDriver) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{Status: work.OutcomeUnknown}, nil
}

func TestWorkRuntimeBindsInstalledOwnerAndSurvivesRestart(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	started := make(chan *work.Engine, 1)
	cfg := WorkConfig{Driver: func(*work.Engine) (work.Driver, error) { return parkedWorkDriver{}, nil }, Ready: func(_ context.Context, e *work.Engine, person, agent string) error {
		if person != r.person || agent != r.agent {
			t.Fatal("work ingress is not owner-bound")
		}
		started <- e
		return nil
	}}
	if err := r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	e := <-started
	goal := work.Goal{ID: "g", Person: r.person, Agent: r.agent, Summary: "complete code change", Done: "the library works", NoServiceReason: "library", Scope: work.GoalScope{Repositories: []string{"repo"}, Includes: "library"}, Criteria: []work.Criterion{{ID: "outcome", Statement: "library works", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "test", Target: "repo"}}}}
	if err := e.Admit(t.Context(), goal); err != nil {
		t.Fatal(err)
	}
	other := goal
	other.ID = "foreign"
	other.Person = "someone-else"
	if err := e.Admit(t.Context(), other); err == nil {
		t.Fatal("foreign owner admitted")
	}
	if err := r.workEngine.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	e = <-started
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Goals) != 1 || s.Goals[0].ID != "g" {
		t.Fatalf("restart lost goal: %+v", s)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = r.workEngine.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchWorkToolUsesBoundOwnerAndRejectsExecutionClaims(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	cfg := WorkConfig{Driver: func(*work.Engine) (work.Driver, error) { return parkedWorkDriver{}, nil }}
	if err = r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	defer r.workEngine.close(t.Context())
	tools := map[string]loop.Tool{"dispatch_work": {Execute: func(context.Context, provider.ToolCall) loop.Result {
		return loop.Result{Code: loop.OK, Output: []byte(`{"existing":true}`)}
	}}}
	declarations, err := r.prepareGoalWorkTools("turn", tools)
	if err != nil || len(declarations) != 1 {
		t.Fatalf("work tool not composed: %v %v", declarations, err)
	}
	if result := tools["dispatch_work"].Execute(t.Context(), provider.ToolCall{}); string(result.Output) != `{"existing":true}` {
		t.Fatal("goal tools replaced existing worker dispatch")
	}
	input := `{"outcome":"fix the code","done":"checks pass","scope":{"repositories":["repo"],"includes":"library","excludes":"service"},"criteria":[{"id":"checked","statement":"library works","check":{"kind":"check","method":"acceptance","target":"repo"}}],"no_service_reason":"library","tasks":[]}`
	call := provider.ToolCall{ID: "dispatch", Name: "dispatch_code_goal", Arguments: input}
	if _, err = tools["dispatch_code_goal"].Validate(call); err != nil {
		t.Fatal(err)
	}
	result := tools["dispatch_code_goal"].Execute(t.Context(), call)
	if result.Code != loop.OK {
		t.Fatalf("dispatch failed: %+v", result)
	}
	snapshot, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Goals) != 1 || snapshot.Goals[0].Person != r.person || snapshot.Goals[0].Agent != r.agent {
		t.Fatalf("unbound goal admission: %+v", snapshot)
	}
	call.Arguments = strings.TrimSuffix(input, "}") + `,"person":"other","state":"done"}`
	if _, err = tools["dispatch_code_goal"].Validate(call); err == nil {
		t.Fatal("execution state/owner injection accepted")
	}
}
