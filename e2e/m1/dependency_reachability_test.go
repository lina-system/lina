// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work"
)

// TestM3SharedAncestorGraphInstalledCore (m3/dependency-reachability.json)
// extends the real worker scenario with an overlapping tail whose shared
// ancestors previously made negative reachability searches exponential.
func TestM3SharedAncestorGraphInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("../m3/dependency-reachability.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Descendants int }
	if err := json.Unmarshal(raw, &scenario); err != nil || scenario.Descendants < 2 {
		t.Fatalf("invalid shared-ancestor scenario: %s %v", raw, err)
	}
	testM3DependencyRelease(t, nil, nil, "", "", dependencyReleaseOptions{sharedAncestors: scenario.Descendants})
}

func sharedAncestorScenario(t *testing.T, raw []byte, count int) []byte {
	t.Helper()
	var graph struct {
		Repository string           `json:"repository"`
		Tasks      []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	previous := []string{"a", "b"}
	for i := range count {
		key := fmt.Sprintf("tail-%02d", i)
		graph.Tasks = append(graph.Tasks, map[string]any{
			"key": key, "what": "Extend the shared result", "why": "Use both predecessors",
			"done": "artifact committed", "areas": []string{"shared/**"},
			"depends_on": previous, "design": []string{"DESIGN.md#work"},
		})
		previous = []string{previous[1], key}
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertSharedAncestorsQueued(t *testing.T, h *harness, store *work.Service, ids []string) {
	t.Helper()
	for _, id := range ids {
		card, err := store.Get(h.ctx, id)
		if err != nil || card.Run != nil || card.WaitReason == nil || card.WaitReason.Kind != "dependency" || card.Scheduling.Reserved {
			t.Fatalf("unmerged shared ancestor released a worker: %+v %v", card, err)
		}
		if _, err := os.Stat(card.Task.Schedule.Workdir); !os.IsNotExist(err) {
			t.Fatalf("queued task allocated a worktree: %s %v", id, err)
		}
	}
}
