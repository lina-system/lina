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
