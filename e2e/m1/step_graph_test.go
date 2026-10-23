// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/runs"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// TestM3StepGraphInstalledCore (m3/step-graph.json) uses the installed Core,
// model work tools and a real fixture worker process. The admitted graph produces
// a committed result, merge, QA, deployment and verified outcome. Restart reads
// the exact graph through the same tool and must not repeat those effects.
func TestM3StepGraphInstalledCore(t *testing.T) {
	t.Parallel()
	installedCodeGoal(t, "step-graph")
}

func TestM3ReadyFrontierInstalledCore(t *testing.T) {
	t.Parallel()
	installedCodeGoal(t, "ready-frontier")
}

type stepGraphScenario struct {
	mixed           bool
	backgroundCalls int
	mu              sync.Mutex
	refusals        []struct{ Kind, Name, Code string }
	call            int
	id              string
	saved           goal.StepGraph
	read            bool
}

func newStepGraphScenario(t *testing.T) *stepGraphScenario {
	t.Helper()
	raw, err := os.ReadFile("../m3/step-graph.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Refusals []struct{ Kind, Name, Code string }
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return &stepGraphScenario{refusals: fixture.Refusals}
}
func (s *stepGraphScenario) respond(t *testing.T, w http.ResponseWriter, request methodologyRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mixed && strings.Contains(request.Instructions, "self-contained background brief") {
		s.backgroundCalls++
		if s.backgroundCalls == 1 {
			emitPersonaCall(w, "read-catalog", "skill_index", map[string]any{})
			return
		}
		if s.backgroundCalls != 2 {
			t.Error("background work repeated")
		}
		emitResponse(w, string(mustJSON(t, runs.Artifact{Body: "Catalog read", Done: true})), true)
		return
	}
	n := s.call
	s.call++
	operation := n / 2
	count := len(s.refusals)
	step := func() goal.Step {
		return goal.Step{ID: "change", Name: "Create output", Done: "Produce result.txt", Repository: "fixture/project", Base: "dev", Area: "result.txt", Worker: "antigravity", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}, DependsOn: []string{}}
	}
	steps := func(p goal.Step) []goal.Step {
		if !s.mixed {
			return []goal.Step{p}
		}
		p.DependsOn = []string{"facts", "time-entry"}
		return []goal.Step{p,
			{ID: "facts", Name: "Read catalog", Kind: goal.BackgroundStep, Done: "Catalog read", Repository: "fixture/project", Resources: goal.Resources{CPU: 0.01, MemoryMB: 1, DiskMB: 1}, DependsOn: []string{}, Background: &runs.Brief{Goal: "Read catalog", Scope: "skill catalog", NotTouch: "files", Done: "Catalog read", Result: "notes", Allow: []string{"skill_index"}, Checks: []runs.Check{{Name: "skill_index", Kind: runs.ToolCalled}}}},
			{ID: "time-entry", Name: "Wait for date", Kind: goal.WaitStep, Done: "Date reached", Repository: "fixture/project", DependsOn: []string{}, Wait: &goal.StepWait{At: 1}},
		}
	}
	if n%2 == 1 {
		switch {
		case operation == 0:
			var receipt struct {
				GoalID string `json:"goal_id"`
			}
			methodologyOutput(t, request, "ok", &receipt)
			s.id = receipt.GoalID
			if s.id == "" {
				t.Error("missing goal receipt")
			}
		case operation <= count:
			var refusal struct{ Code string }
			methodologyOutput(t, request, "invalid_arguments", &refusal)
			if refusal.Code != s.refusals[operation-1].Code {
				t.Errorf("refusal %d: %s", operation-1, refusal.Code)
			}
		case operation == count+1:
			var empty goal.StepGraph
			methodologyOutput(t, request, "ok", &empty)
			if empty.GoalID != s.id || empty.Revision != 0 || len(empty.Steps) != 0 {
				t.Errorf("refusal admitted a partial graph: %+v", empty)
			}
		case operation == count+2 || operation == count+3:
			methodologyOutput(t, request, "ok", &s.saved)
			want := step()
			if operation == count+3 {
				want.Name = "Prepare output"
			}
			if s.saved.GoalID != s.id || s.saved.Revision != uint64(operation-count-1) || !reflect.DeepEqual(s.saved.Steps, steps(want)) {
				t.Errorf("missing complete graph: %+v", s.saved)
			}
		case operation == count+4:
			var got goal.StepGraph
			methodologyOutput(t, request, "ok", &got)
			if !reflect.DeepEqual(s.saved, got) {
				t.Errorf("restart changed graph: %+v -> %+v", s.saved, got)
			}
			s.read = true
		default:
			t.Errorf("unexpected graph operation %d", operation)
		}
		emitResponse(w, "Step operation checked.", true)
		return
	}
	emit := func(name string, args any) {
		if !toolReachable(request.Tools, name) {
			t.Errorf("installed Core did not expose %s", name)
		}
		emitPersonaCall(w, fmt.Sprintf("graph-operation-%d", n), name, args)
	}
	switch {
	case operation == 0:
		emit("dispatch_code_goal", map[string]any{"outcome": "Produce and run the result", "done": "The running service returns the result", "scope": map[string]any{"repositories": []string{"fixture/project"}, "includes": "result.txt", "excludes": "design.md"}, "criteria": []any{map[string]any{"id": "result", "statement": "The result matches the request", "check": map[string]any{"kind": "check", "method": "acceptance", "target": "fixture/project"}}}, "service_target": "fixture-service", "tasks": []any{}})
	case operation == count+1 || operation == count+4:
		emit("read_step_graph", map[string]any{"goal_id": s.id})
	default:
		p := step()
		revision := 0
		if operation <= count {
			tc := s.refusals[operation-1]
			switch tc.Kind {
			case "cycle":
				p.DependsOn = []string{"change"}
			case "unknown":
				p.DependsOn = []string{"missing"}
			case "area":
				p.Area = "../result.txt"
			default:
				p.Name = tc.Name
			}
		} else if operation == count+3 {
			p.Name = "Prepare output"
			revision = 1
		}
		proposed := []goal.Step{p}
		if operation > count {
			proposed = steps(p)
		}
		emit("submit_step_graph", map[string]any{"goal_id": s.id, "expected_revision": revision, "steps": proposed})
	}
}

func (s *stepGraphScenario) verify(t *testing.T, g goal.Goal) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mixed {
		if s.backgroundCalls != 2 || len(g.Tasks) != 3 {
			t.Fatalf("missing mixed work: background=%d tasks=%d", s.backgroundCalls, len(g.Tasks))
		}
		for _, task := range g.Tasks {
			if task.ID == "change" {
				continue
			}
			if task.Stage != goal.TaskDone || !task.Result.Passed || task.Result.Ref == "" || task.Attempt != 1 {
				t.Fatalf("step did not land exactly once: %+v", task)
			}
		}
	}
	if g.StepGraph == nil || !reflect.DeepEqual(*g.StepGraph, s.saved) || !s.read || s.call != 2*(len(s.refusals)+5) {
		t.Fatalf("missing graph revision/restart evidence: goal=%+v read=%t calls=%d", g.StepGraph, s.read, s.call)
	}
}
