// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestGroupChangeIsStaleAndAnswerRollsBackWholeGroup(t *testing.T) {
	f := setup(t)
	f.register(t, true, nil)
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	groups, err := f.service.Groups(t.Context(), f.call.Auth.Person)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	shown := groups[0]
	f.call.Key = "second"
	if _, err := f.registry.Dispatch(t.Context(), f.call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatal(err)
	}
	if err := f.service.Answer(t.Context(), shown.ID, owner(f), authority.Decision{Choice: authority.Once, GroupRevision: shown.Revision}); !errors.Is(err, authority.ErrStale) {
		t.Fatalf("changed group=%v", err)
	}
	pending, err := f.service.Pending(t.Context())
	if err != nil || len(pending) != 2 {
		t.Fatalf("partial approval: %v %v", pending, err)
	}
	groups, err = f.service.Groups(t.Context(), f.call.Auth.Person)
	if err != nil || len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
	if err := f.service.Answer(t.Context(), groups[0].ID, owner(f), authority.Decision{Choice: authority.Always, GroupRevision: groups[0].Revision}); !errors.Is(err, authority.ErrChoice) {
		t.Fatalf("invalid group choice=%v", err)
	}
	pending, _ = f.service.Pending(t.Context())
	if len(pending) != 2 {
		t.Fatal("invalid choice partially committed")
	}
	if err := f.service.Answer(t.Context(), groups[0].ID, owner(f), authority.Decision{Choice: authority.Once, GroupRevision: groups[0].Revision}); err != nil {
		t.Fatal(err)
	}
	pending, _ = f.service.Pending(t.Context())
	if len(pending) != 0 {
		t.Fatal("whole group did not commit")
	}
}
