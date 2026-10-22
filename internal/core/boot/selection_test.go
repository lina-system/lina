// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
)

func TestInputSelectionCommitsWithAdmissionAndIgnoresReplayAndChannels(t *testing.T) {
	r, session, first, second := managementFixture(t)
	selectFirst := func() {
		t.Helper()
		if err := r.agents.Select(t.Context(), first, session.PersonID); err != nil {
			t.Fatal(err)
		}
	}
	selected := func(want string) {
		t.Helper()
		a, err := r.agents.Selected(t.Context(), session.PersonID)
		if err != nil || a.ID != want {
			t.Fatalf("selection=%+v, want %s: %v", a, want, err)
		}
	}
	selectFirst()
	input := &conversationAdmission{db: r.db, person: session.PersonID, agent: second,
		input: inbox.Input{Key: inbox.Key{Conversation: "main:" + second, Source: "client", NativeID: "fixture"}, Body: []byte("fixture")}}
	injected := errors.New("ledger publication failed")
	if err := input.Write(t.Context(), func(*canon.Tx) error { return injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	selected(first)
	if err := input.Write(t.Context(), func(*canon.Tx) error { return nil }); err != nil || !input.receipt.Inserted {
		t.Fatalf("failed admission was not rolled back: %v", err)
	}
	selected(second)
	selectFirst()
	if err := input.Write(t.Context(), func(*canon.Tx) error { t.Fatal("replay wrote ledger"); return nil }); !errors.Is(err, errAdmittedInput) {
		t.Fatal(err)
	}
	selected(first)
	channel := &conversationAdmission{db: r.db,
		input: inbox.Input{Key: inbox.Key{Conversation: "main:" + second, Source: "channel", NativeID: "fixture"}, Body: []byte("fixture")}}
	if err := channel.Write(t.Context(), func(*canon.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	selected(first)
}
