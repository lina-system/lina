// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
)

func TestTodoPlanningEditsPreserveDelegatedCompletion(t *testing.T) {
	for _, delegation := range []struct {
		name string
		todo records.Todo
	}{
		{"worker", records.Todo{Assignee: &records.Assignee{Kind: "worker", ID: "worker-1"}}},
		{"agent", records.Todo{Assignee: &records.Assignee{Kind: "agent", ID: "agent-1"}}},
		{"pull-request", records.Todo{PullRequest: "https://example.invalid/pull/1"}},
		{"observation", records.Todo{ExecutionState: "landed"}},
	} {
		t.Run(delegation.name, func(t *testing.T) {
			for _, state := range []string{"completed", `"completed"`} {
				beforeTodo := delegation.todo
				beforeTodo.State = "completed"
				before, err := records.WithTodo("---", beforeTodo)
				if err != nil {
					t.Fatal(err)
				}
				// Both the legacy bare spelling and typed JSON spelling are valid.
				before = strings.Replace(before, `state: "completed"`, "state: "+state, 1)
				next := delegation.todo
				next.State = "completed"
				next.Tags = []string{"reviewed"}
				next.Priority = "high"
				next.Checklist = []records.ChecklistItem{{Text: "Attach the result", Checked: true}}
				after, err := records.WithTodo(before, next)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateTodoEdit(before, after, next); err != nil {
					t.Errorf("planning edit preserving %s completion: %v", state, err)
				}
				// An unchanged completed state must not bypass engine observations.
				next.ExecutionState = "invented"
				after, err = records.WithTodo(before, next)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateTodoEdit(before, after, next); !errors.Is(err, records.ErrInvalid) {
					t.Errorf("execution observation changed: %v", err)
				}
			}
			for _, state := range []string{"", "backlog", "open", "started", "canceled"} {
				beforeTodo := delegation.todo
				beforeTodo.State = state
				before, err := records.WithTodo("---", beforeTodo)
				if err != nil {
					t.Fatal(err)
				}
				for _, clear := range []bool{false, true} {
					next := delegation.todo
					if clear {
						next.Assignee, next.PullRequest = nil, ""
					}
					next.State = "completed"
					after, err := records.WithTodo(before, next)
					if err != nil {
						t.Fatal(err)
					}
					if err := validateTodoEdit(before, after, next); !errors.Is(err, records.ErrInvalid) {
						t.Errorf("invented completion from %q (clear=%t): %v", state, clear, err)
					}
				}
			}
		})
	}
}

func TestTodoPersonalCompletionAndNewDelegation(t *testing.T) {
	for _, kind := range []string{"person", "agent", "worker"} {
		next := records.Todo{State: "completed", Assignee: &records.Assignee{Kind: kind, ID: "assignee-1"}}
		after, err := records.WithTodo("---", next)
		if err != nil {
			t.Fatal(err)
		}
		err = validateTodoEdit("---\nstate: open", after, next)
		if (err == nil) != (kind == "person") {
			t.Errorf("new %s completion: %v", kind, err)
		}
	}
}

func TestTodoToolCannotInventWorkCompletion(t *testing.T) {
	for _, before := range []string{
		"---\nassignee: {\"kind\":\"worker\",\"id\":\"worker-1\"}",
		"---\nassignee: {\"kind\":\"agent\",\"id\":\"agent-1\"}",
		"---\npull_request: \"https://example.invalid/pull/1\"",
		"---\nexecution_state: \"running\"",
	} {
		next := records.Todo{State: "completed"}
		after, err := records.WithTodo(before, next)
		if err != nil {
			t.Fatal(err)
		}
		if validateTodoEdit(before, after, next) == nil {
			t.Fatal("model completed delegated work", before)
		}
	}
	if validateTodoEdit("---", "---\nexecution_state: \"done\"", records.Todo{}) == nil {
		t.Fatal("model invented execution observation")
	}
}
