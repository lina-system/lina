// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Each task depends on its previous two tasks, so every pair is ordered but
// negative searches encounter exponentially many paths to shared ancestors.
func sharedAncestorTasks(n int) []Task {
	tasks := make([]Task, n)
	for i := range tasks {
		var deps []string
		for j := max(0, i-2); j < i; j++ {
			deps = append(deps, tasks[j].ID)
		}
		tasks[i] = scheduled(fmt.Sprintf("task-%03d", i), deps...)
		tasks[i].Schedule.Areas = []string{"shared/**"}
	}
	return tasks
}

func TestDependencyReachabilityTraversalBound(t *testing.T) {
	t.Parallel()
	const n = 256
	tasks := sharedAncestorTasks(n)
	graph := map[string][]string{}
	edges := 0
	for _, task := range tasks {
		graph[task.ID] = task.Schedule.DependsOn
		edges += len(task.Schedule.DependsOn)
	}
	verticesRead, edgesRead := 0, 0
	depends := dependencyReachability(func(id string) []string {
		verticesRead++
		edgesRead += len(graph[id])
		if verticesRead > n*n || edgesRead > n*edges {
			t.Fatalf("shared-ancestor traversal exceeded graph bound: vertices=%d (max %d), edges=%d (max %d)", verticesRead, n*n, edgesRead, n*edges)
		}
		return graph[id]
	})
	// Ascending order forces negative queries before the positive reverse query,
	// as graph admission does. Repeating queries must reuse the bounded work.
	for range 2 {
		for i := range tasks {
			for j := i + 1; j < n; j++ {
				if depends(tasks[i].ID, tasks[j].ID) || !depends(tasks[j].ID, tasks[i].ID) {
					t.Fatalf("wrong dependency ordering for %d, %d", i, j)
				}
			}
		}
	}
	t.Logf("vertices=%d/%d edges=%d/%d", verticesRead, n*n, edgesRead, n*edges)
}

func TestSharedAncestorGraphAdmission(t *testing.T) {
	t.Parallel()
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			tasks := sharedAncestorTasks(256)
			if reverse {
				slices.Reverse(tasks)
			}
			if err := ValidateSchedule(tasks); err != nil {
				t.Fatal(err)
			}
			s, _ := testStore(t)
			for range 2 {
				if err := s.Schedule(t.Context(), tasks); err != nil {
					t.Fatal(err)
				}
			}
			cards, err := s.Cards(t.Context())
			if err != nil || len(cards) != len(tasks) {
				t.Fatalf("admission: cards=%d err=%v", len(cards), err)
			}
			for _, task := range tasks {
				events, err := s.Ledger(t.Context(), task.ID)
				if err != nil || len(events) != 1 || !reflect.DeepEqual(*events[0].Task, task) {
					t.Fatalf("admission/retry changed task %s: %+v %v", task.ID, events, err)
				}
			}
		})
	}
}

func TestSharedAncestorGraphRejectsAtomically(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing", "cycle", "unordered"} {
		t.Run(kind, func(t *testing.T) {
			s, db := testStore(t)
			existing := scheduled("existing")
			if err := s.Schedule(t.Context(), []Task{existing}); err != nil {
				t.Fatal(err)
			}
			before, err := s.Cards(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := sharedAncestorTasks(32)
			switch kind {
			case "missing":
				tasks[0].Schedule.DependsOn = []string{"absent"}
			case "cycle":
				tasks[0].Schedule.DependsOn = []string{tasks[len(tasks)-1].ID}
			case "unordered":
				fork := scheduled("fork", tasks[len(tasks)-2].ID)
				fork.Schedule.Areas = []string{"shared/file.go"}
				tasks = append(tasks, fork)
			}
			if err := ValidateSchedule(tasks); !errors.Is(err, ErrInvalid) {
				t.Fatalf("preflight accepted %s: %v", kind, err)
			}
			if err := s.Schedule(t.Context(), tasks); !errors.Is(err, ErrInvalid) {
				t.Fatalf("transaction accepted %s: %v", kind, err)
			}
			after, err := s.Cards(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejection changed cards: %+v %v", after, err)
			}
			var count int
			err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_ledger").Scan(&count)
			})
			if err != nil || count != 1 {
				t.Fatalf("partial ledger admission: count=%d err=%v", count, err)
			}
		})
	}
}
