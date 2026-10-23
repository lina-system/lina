// SPDX-License-Identifier: Apache-2.0

package agents_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestSelectionIsDurableScopedAndRejectsUnavailableAgents(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	a, b := create(t, s, ""), create(t, s, "")
	if err := s.Select(t.Context(), a.ID, "person"); err != nil {
		t.Fatal(err)
	}
	if err := s.Select(t.Context(), b.ID, "another"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), b.ID, "person", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{b.ID, "missing"} {
		if err := s.Select(t.Context(), id, "person"); !errors.Is(err, agents.ErrNotFound) {
			t.Fatalf("unavailable selection accepted: %v", err)
		}
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Selected(t.Context(), "person"); err != nil || got != a {
		t.Fatalf("selection lost or changed by another person or refusal: %+v %v", got, err)
	}
	for _, person := range []string{"another", "unknown"} {
		if _, err := reopened.Selected(t.Context(), person); !errors.Is(err, agents.ErrNotFound) {
			t.Fatalf("unavailable selection returned for %s: %v", person, err)
		}
	}
}
