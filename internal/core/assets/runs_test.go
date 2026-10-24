// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/kit/materials"
)

func TestRunResultRetryKeepsIdentityAndPriorRevision(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	id := strings.Repeat("a", 32)
	first, err := s.SaveRunResult(t.Context(), id, []byte("gathered"), actor)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.SaveRunResult(t.Context(), id, []byte("gathered"), actor)
	if err != nil || same.ID != first.ID || same.Revision != first.Revision {
		t.Fatalf("retry=%+v error=%v", same, err)
	}
	next, err := s.SaveRunResult(t.Context(), id, []byte("completed"), actor)
	if err != nil || next.ID != id || next.Revision <= first.Revision {
		t.Fatalf("next=%+v error=%v", next, err)
	}
	b, err := s.Read(t.Context(), first.ID, first.Revision)
	if err != nil || string(b) != "gathered" {
		t.Fatalf("old=%s error=%v", b, err)
	}
	if _, err := s.SaveRunResult(t.Context(), id, []byte("replacement"), assets.Actor{PersonID: "different-person", AgentID: actor.AgentID}); !errors.Is(err, assets.ErrInvalid) {
		t.Fatalf("changed owner=%v", err)
	}
}
func TestCitationUsesNamedRevisionAndDeletedEvidenceDoesNotResolve(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := importAsset(t, s, "original passage")
	anchor := materials.Anchor{AssetID: a.ID, Revision: a.Revision, Start: 0, End: 8, Hash: a.Hash}
	if _, err := s.Edit(t.Context(), a.ID, a.Revision, strings.NewReader("new passage"), actor); err != nil {
		t.Fatal(err)
	}
	checks, err := materials.Check(t.Context(), s, []materials.Anchor{anchor})
	if err != nil || !checks[0].Resolved {
		t.Fatalf("historical citation=%+v error=%v", checks, err)
	}
	if err := s.Trash(t.Context(), a.ID, actor); err != nil {
		t.Fatal(err)
	}
	checks, err = materials.Check(t.Context(), s, []materials.Anchor{anchor})
	if err != nil || checks[0].Resolved {
		t.Fatalf("deleted citation=%+v error=%v", checks, err)
	}
}
