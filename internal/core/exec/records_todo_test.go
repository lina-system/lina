// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"github.com/thisisjun786/lina/internal/core/records"
	"testing"
)

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
