// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"testing"
)

func TestBatchPublishesOneRevisionAndRejectsAllOnConflict(t *testing.T) {
	s, root, _ := setup(t)
	changes := []Change{{ID: "project", Path: "projects/work.md", Data: doc("project", "Work")}, {ID: "todo", Path: "projects/task.md", Data: doc("todo", "Task")}}
	rev, err := s.CommitBatch(t.Context(), changes, actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		_, got, err := s.Read(t.Context(), c.ID)
		if err != nil || got != rev {
			t.Fatal(got, err)
		}
	}
	if git(t, root, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("batch was not one saved version")
	}
	changes[0] = Change{ID: "new", Path: "projects/new.md", Data: doc("new", "New")}
	changes[1].Base = "stale"
	if _, err = s.CommitBatch(t.Context(), changes, actor); !errors.Is(err, ErrStaleRevision) {
		t.Fatal(err)
	}
	if _, _, err = s.Read(t.Context(), "new"); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial batch", err)
	}
}

func TestBatchRecoveryRetainsEveryRecord(t *testing.T) {
	for _, stage := range []string{"intent", "file", "index", "ref", "checkpoint"} {
		t.Run(stage, func(t *testing.T) {
			s, root, db := setup(t)
			s.PublicationHook = func(at string) error {
				if at == stage {
					return errors.New("cut")
				}
				return nil
			}
			changes := []Change{{ID: "one", Path: "projects/one.md", Data: doc("one", "One")}, {ID: "two", Path: "projects/two.md", Data: doc("two", "Two")}}
			if _, err := s.CommitBatch(t.Context(), changes, actor); !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			reopened, err := Open(root, db)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			for _, c := range changes {
				if _, _, err = reopened.Read(t.Context(), c.ID); err != nil {
					t.Fatal(err)
				}
			}
			if git(t, root, "rev-list", "--count", "HEAD") != "1" {
				t.Fatal("recovery duplicated batch")
			}
			git(t, root, "fsck", "--strict")
		})
	}
}

func TestBatchTodoLinksUseTheCompleteCandidate(t *testing.T) {
	s, root, _ := setup(t)
	changes := []Change{
		{ID: "second", Path: "projects/second.md", Data: todoDocument("second", "belongs_to: [\"project\"]\nblocked_by: [\"first\"]\n")},
		{ID: "first", Path: "projects/first.md", Data: todoDocument("first", "belongs_to: [\"project\"]\n")},
		{ID: "project", Path: "projects/project.md", Data: []byte("---\nid: \"project\"\nkind: project\nstate: active\nperson_id: \"person\"\n---\n\n# Project\n")},
	}
	if _, err := s.CommitBatch(t.Context(), changes, actor); err != nil {
		t.Fatal("valid references inside one planning publication rejected:", err)
	}
	if git(t, root, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("linked records were not one publication")
	}
}

func TestBatchTodoLinksRejectInvalidCandidatesWithoutPublishing(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		want                error
	}{
		{"cycle", "blocked_by: [\"second\"]\n", "blocked_by: [\"first\"]\n", ErrDependencyCycle},
		{"missing", "blocked_by: [\"missing\"]\n", "", ErrInvalid},
		{"wrong-parent-kind", "belongs_to: [\"second\"]\n", "", ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := setup(t)
			before, _ := s.Head(t.Context())
			_, err := s.CommitBatch(t.Context(), []Change{
				{ID: "first", Path: "projects/first.md", Data: todoDocument("first", tc.first)},
				{ID: "second", Path: "projects/second.md", Data: todoDocument("second", tc.second)},
			}, actor)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if after, _ := s.Head(t.Context()); after != before {
				t.Fatal("invalid batch changed published history")
			}
		})
	}
}

func TestBatchTodoLinksRejectCycleThroughPublishedRecords(t *testing.T) {
	s, _, _ := setup(t)
	first := Change{ID: "first", Path: "projects/first.md", Data: todoDocument("first", "")}
	first.Base = commit(t, s, first)
	commit(t, s, Change{ID: "published", Path: "projects/published.md", Data: todoDocument("published", "blocked_by: [\"first\"]\n")})
	before, _ := s.Head(t.Context())
	first.Data = todoDocument("first", "blocked_by: [\"published\"]\n")
	_, err := s.CommitBatch(t.Context(), []Change{first, {ID: "extra", Path: "projects/extra.md", Data: todoDocument("extra", "")}}, actor)
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("cycle through published prerequisite: %v", err)
	}
	if after, _ := s.Head(t.Context()); after != before {
		t.Fatal("cycle changed published history")
	}
}

func TestBatchTodoLinksRejectForeignParent(t *testing.T) {
	s, _, _ := setup(t)
	before, _ := s.Head(t.Context())
	_, err := s.CommitBatch(t.Context(), []Change{
		{ID: "task", Path: "projects/task.md", Data: todoDocument("task", "belongs_to: [\"project\"]\n")},
		{ID: "project", Path: "projects/project.md", Data: []byte("---\nid: \"project\"\nkind: project\nstate: active\nperson_id: \"other\"\n---\n\n# Project\n")},
	}, actor)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-person parent accepted: %v", err)
	}
	if after, _ := s.Head(t.Context()); after != before {
		t.Fatal("cross-person link changed published history")
	}
}
