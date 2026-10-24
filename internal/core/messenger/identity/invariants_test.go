// SPDX-License-Identifier: Apache-2.0
package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestEndedLinkRetainsHistoricalAmbiguity(t *testing.T) {
	t.Parallel()
	const number = "+821012340001"
	people := identity.StaticPeople{
		{Record: "a", Identifiers: []identity.SeenIdentifier{phone(number, "contact")}},
		{Record: "b"},
	}
	f := newFixture(t, people)
	observeAt(t, f, discord("sender"), at(8), "", phone(number, "profile"))
	people[1].Identifiers = []identity.SeenIdentifier{phone(number, "contact")}
	if link := links(t, f, discord("sender"))[0]; !link.Ambiguous {
		t.Fatal("shared phone did not make the existing link ambiguous")
	}
	observeAt(t, f, imessage(number), at(10), "")
	link := links(t, f, discord("sender"))[0]
	if !link.ValidThrough.Equal(at(10)) || !link.Ambiguous || link.Usable(at(9)) {
		t.Fatalf("ending a link cleared its historical conflict: %+v", link)
	}
	confirmed, err := f.store.Confirm(t.Context(), discord("sender"), "a", "A used this account")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Usable(at(9)) || confirmed.Usable(at(10)) {
		t.Fatalf("confirmation must settle ambiguity without reopening validity: %+v", confirmed)
	}
}

func TestLaterIdentifierConflictMakesExistingLinkUnusable(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "a", Identifiers: []identity.SeenIdentifier{phone("+821012340002", "contact")}},
		{Record: "b", Identifiers: []identity.SeenIdentifier{mail("b@example.com", "contact")}},
	}
	f := newFixture(t, people)
	observeAt(t, f, discord("sender"), at(8), "", phone("+821012340002", "profile"))
	observeAt(t, f, discord("sender"), at(9), "", mail("b@example.com", "profile"))
	link := links(t, f, discord("sender"))[0]
	if !link.Ambiguous || link.Usable(at(9)) {
		t.Fatalf("conflicting email left the original phone link usable: %+v", link)
	}
	question, found, err := f.store.Question(t.Context(), discord("sender"))
	if err != nil || !found || len(question.Candidates) != 2 {
		t.Fatalf("question = %+v, %v, %v", question, found, err)
	}
}

func TestConfirmationPreservesBoundsAcrossStatesAndRecords(t *testing.T) {
	for _, action := range []string{"active", "rejected", "removed", "different record"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			const number = "+821012340003"
			f := newFixture(t, identity.StaticPeople{{Record: "a", Identifiers: []identity.SeenIdentifier{phone(number, "contact")}}, {Record: "b"}})
			observeAt(t, f, discord("sender"), at(8), "", phone(number, "profile"))
			observeAt(t, f, imessage(number), at(10), "")
			original := links(t, f, discord("sender"))[0]
			target := "a"
			switch action {
			case "rejected":
				if err := f.store.Reject(t.Context(), original.ID); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if _, err := f.store.Remove(t.Context(), original.ID); err != nil {
					t.Fatal(err)
				}
			case "different record":
				target = "b"
			}
			confirmed, err := f.store.Confirm(t.Context(), discord("sender"), target, "the owner resolved this account")
			if err != nil {
				t.Fatal(err)
			}
			if !confirmed.ValidFrom.Equal(original.ValidFrom) || !confirmed.ValidThrough.Equal(original.ValidThrough) || !confirmed.Usable(at(9)) || confirmed.Usable(at(10)) || confirmed.Usable(at(7)) {
				t.Fatalf("confirmation changed the ownership period: %+v", confirmed)
			}
		})
	}
}

type interruptedRemoval struct {
	store *retract.Store
	after bool
}

func (r interruptedRemoval) Forget(ctx context.Context, req retract.Request) error {
	if r.after {
		if err := r.store.Forget(ctx, req); err != nil {
			return err
		}
	}
	return errors.New("simulated process interruption")
}

func TestRepairSurvivesInterruptionAndRestart(t *testing.T) {
	for _, action := range []string{"remove", "correct", "confirm", "cutoff", "transaction only"} {
		for _, after := range []bool{false, true} {
			name := action + "/before removal"
			if after {
				name = action + "/after removal"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				const number = "+821012340004"
				people := identity.StaticPeople{{Record: "a", Identifiers: []identity.SeenIdentifier{phone(number, "contact")}}, {Record: "b"}}
				f := newFixture(t, people)
				observeAt(t, f, discord("sender"), at(8), "", phone(number, "profile"))
				link := links(t, f, discord("sender"))[0]
				f.now = at(11)
				item := f.item(t, "a", "affected")
				if err := f.store.RecordArtifact(t.Context(), link.ID, "item", item.ID); err != nil {
					t.Fatal(err)
				}
				unrelated := f.item(t, "b", "unrelated")
				f.store = identity.Open(f.db, "person", people, interruptedRemoval{store: f.removal, after: after}).WithClock(func() time.Time { return f.now })
				var err error
				switch action {
				case "remove":
					_, err = f.store.Remove(t.Context(), link.ID)
				case "correct":
					_, err = f.store.Correct(t.Context(), link.ID, "b")
				case "confirm":
					_, err = f.store.Confirm(t.Context(), discord("sender"), "b", "B")
				case "cutoff":
					err = f.store.Observe(t.Context(), identity.Observation{Identity: imessage(number), At: at(10)})
				case "transaction only":
					observer, prepareErr := f.store.Prepare(t.Context())
					if prepareErr != nil {
						t.Fatal(prepareErr)
					}
					if writeErr := f.db.Write(t.Context(), func(tx *canon.Tx) error {
						return observer.ObserveTx(t.Context(), tx, identity.Observation{Identity: imessage(number), At: at(10)})
					}); writeErr != nil {
						t.Fatal(writeErr)
					}
					// Simulate exit after admission commits and before Observer.Repair.
					err = errors.New("simulated exit before repair")
				}
				if err == nil {
					t.Fatal("interruption was not surfaced")
				}
				f.store = identity.Open(f.db, "person", people, f.removal).WithClock(func() time.Time { return f.now })
				for range 2 {
					if _, err := f.store.Rematch(t.Context()); err != nil {
						t.Fatal(err)
					}
					if visible(t, f, item.ID) {
						t.Fatal("restart left a wrong-match item visible")
					}
					if !visible(t, f, unrelated.ID) {
						t.Fatal("repair removed unrelated memory")
					}
				}
			})
		}
	}
}

func TestConfirmationCannotBypassCutoffThroughAnOlderProposal(t *testing.T) {
	t.Parallel()
	const number = "+821012340005"
	people := identity.StaticPeople{
		{Record: "a", Names: []string{"Alex"}},
		{Record: "b", Identifiers: []identity.SeenIdentifier{phone(number, "contact")}},
	}
	f := newFixture(t, people)
	observeAt(t, f, discord("sender"), at(7), "Alex")
	observeAt(t, f, discord("sender"), at(8), "", phone(number, "profile"))
	observeAt(t, f, imessage(number), at(10), "")
	confirmed, err := f.store.Confirm(t.Context(), discord("sender"), "a", "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.ValidFrom.Equal(at(8)) || !confirmed.ValidThrough.Equal(at(10)) || confirmed.Usable(at(11)) {
		t.Fatalf("proposal bypassed the known ownership window: %+v", confirmed)
	}
}

func TestRemovedLinkStillRecordsLaterIdentifierMovement(t *testing.T) {
	t.Parallel()
	const number = "+821012340006"
	f := newFixture(t, identity.StaticPeople{{Record: "a", Identifiers: []identity.SeenIdentifier{phone(number, "contact")}}})
	observeAt(t, f, discord("sender"), at(8), "", phone(number, "profile"))
	link := links(t, f, discord("sender"))[0]
	if _, err := f.store.Remove(t.Context(), link.ID); err != nil {
		t.Fatal(err)
	}
	observeAt(t, f, imessage(number), at(10), "")
	confirmed, err := f.store.Confirm(t.Context(), discord("sender"), "a", "A")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.ValidThrough.Equal(at(10)) || confirmed.Usable(at(11)) {
		t.Fatalf("reactivation lost movement recorded while removed: %+v", confirmed)
	}
}
