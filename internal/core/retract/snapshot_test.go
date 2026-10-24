// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"errors"
	"testing"
)

func TestSnapshotFiltersTargetsEvidenceAndStaleDerivedData(t *testing.T) {
	t.Parallel()
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	before, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	refs := []Ref{{Kind: Memory, ID: "memory-1"}, {Kind: Event, ID: "event-1"}}
	if !before.DerivedValid(before.Epoch, refs) {
		t.Fatal("active evidence was excluded")
	}
	if err := s.Forget(t.Context(), Request{Person: "person-1", Targets: refs[:1]}); err != nil {
		t.Fatal(err)
	}
	after, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if after.Visible(ref) {
			t.Fatalf("exposed %+v", ref)
		}
	}
	if !after.Visible(Ref{Kind: Event, ID: "unrelated"}) {
		t.Fatal("unrelated event suppressed")
	}
	if after.DerivedValid(before.Epoch, nil) || after.DerivedValid(after.Epoch, refs) {
		t.Fatal("stale or revoked derived data accepted")
	}
	if after.CanExtract([]string{"event-1"}, nil) || after.CanExtract([]string{"new-event"}, []string{"conversation-1"}) {
		t.Fatal("forgotten item could be extracted again")
	}
	if !after.CanExtract([]string{"other-event"}, []string{"other-conversation"}) {
		t.Fatal("unrelated extraction refused")
	}
	if err := s.Validate(t.Context(), before.Epoch); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("stale request admitted: %v", err)
	}
	if err := s.Validate(t.Context(), after.Epoch); err != nil {
		t.Fatal(err)
	}
	other, err := s.Snapshot(t.Context(), "person-2", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if !other.Visible(refs[0]) {
		t.Fatal("one person's removal crossed person boundary")
	}
	var uninitialized Snapshot
	if uninitialized.Visible(refs[0]) || uninitialized.DerivedValid(0, nil) || uninitialized.CanExtract(nil, nil) {
		t.Fatal("uninitialized snapshot allowed input")
	}
}
