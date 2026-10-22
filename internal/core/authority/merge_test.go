// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
)

func TestMergeAuthorityPreservesBaselineAndUnknownReservation(t *testing.T) {
	f := setup(t)
	a := authority.NewMerges(f.store, 7)
	b := authority.MergeBinding{ID: "task", Person: "person", Agent: "agent", Repository: "o/r", Number: 140, Base: "dev", Clone: "/clone", Protected: map[string]string{"refs/heads/dev": "base"}}
	if err := a.Bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := a.Accept(t.Context(), "task", "person", "head"); err != nil {
		t.Fatal(err)
	}
	if err := a.Begin(t.Context(), "task", "person", "head", map[string]string{"refs/heads/dev": "changed"}); !errors.Is(err, authority.ErrProtectedRef) {
		t.Fatalf("mismatch=%v", err)
	}
	if err := a.Begin(t.Context(), "task", "person", "head", b.Protected); err != nil {
		t.Fatal(err)
	}
	a = authority.NewMerges(f.store, 7)
	if err := a.Begin(t.Context(), "task", "person", "head", b.Protected); err == nil {
		t.Fatal("unknown merge repeated")
	}
	if err := a.Accept(t.Context(), "task", "person", "new-head"); err == nil {
		t.Fatal("unknown merge reauthorized")
	}
	b.Protected["refs/heads/dev"] = "changed"
	if err := a.Bind(t.Context(), b); err == nil {
		t.Fatal("baseline overwritten")
	}
	if _, err := a.Get(t.Context(), "task", "other"); err == nil {
		t.Fatal("foreign person read")
	}
	b.ID = "second"
	b.Protected["refs/heads/dev"] = "base"
	if err := a.Bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := a.Accept(t.Context(), "second", "person", "head"); err != nil {
		t.Fatal(err)
	}
	if err := a.Begin(t.Context(), "second", "person", "head", b.Protected); err == nil {
		t.Fatal("unknown target reservation bypassed")
	}
}
