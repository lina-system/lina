// SPDX-License-Identifier: Apache-2.0
package ledger_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/secret"
)

func messengerInput(id string) ledger.Input {
	ref := ledger.MessengerRef{Platform: "discord", Account: "account-1", Chat: "dm-1", Message: "m-1"}
	return ledger.Input{ID: id, ConversationID: "messenger:discord:account-1:dm-1", PersonID: "person",
		// A messenger record is held by its source; no agent participates.
		AgentID:   "messenger:discord:account-1",
		Surface:   ledger.Surface{Kind: "messenger", Platform: ref.Platform, Account: ref.Account, ChatID: ref.Chat},
		Messenger: &ref, OccurredAt: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
		RecordedAt: time.Date(2026, 10, 24, 9, 0, 1, 0, time.UTC), TimeQuality: "recorded",
		Locator: "messenger:discord:account-1:dm-1:m-1", Body: "hello https://example.com/x?token=synthetic-abc&page=2"}
}

func TestMessengerRecordAdmissionKeepsRouteAndReference(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	s := ledger.New(db, nil)
	in := messengerInput("record-1")
	in.Body = "answer is 42"
	first := admit(t, s, guard.EntryMessengerPerson, in)
	if first.SpeakerKind != guard.Person || first.PersonID != "person" || first.Origin.Kind != "messenger" {
		t.Fatalf("person record: %+v", first)
	}
	if first.Origin.Messenger == nil || *first.Origin.Messenger != *in.Messenger {
		t.Fatalf("legacy reference: %+v", first.Origin.Messenger)
	}
	if first.Surface.Account != "account-1" || first.Surface.ChatID != "dm-1" {
		t.Fatalf("surface: %+v", first.Surface)
	}
	reply := messengerInput("record-2")
	reply.PersonID = ""
	reply.Body = "reply"
	reply.Relations = []ledger.Relation{{TargetID: first.ID, Kind: "replies_to"}, {TargetID: first.ID, Kind: "in_thread"}}
	admit(t, s, guard.EntryMessengerParticipant, reply)
	got, err := s.Get(t.Context(), reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SpeakerKind != guard.ExternalParticipant || got.PersonID != "" || len(got.Relations) != 2 {
		t.Fatalf("participant record: %+v", got)
	}
	if got.Origin.Messenger == nil || got.Origin.Messenger.Message != "m-1" {
		t.Fatalf("readback reference: %+v", got.Origin.Messenger)
	}
	// The relation kinds outside the messenger set are still refused.
	other := messengerInput("record-3")
	other.PersonID = ""
	other.Relations = []ledger.Relation{{TargetID: first.ID, Kind: "replies_to", Ordinal: 1}}
	if _, err := s.Admit(t.Context(), guard.EntryMessengerParticipant, other); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("ordered reply accepted: %v", err)
	}
}

func TestMessengerAdmissionFixesSpeakerKindFromTheEntry(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	s := ledger.New(db, nil)
	for i, tc := range []struct {
		entry guard.Entry
		kind  guard.SpeakerKind
	}{
		{guard.EntryMessengerPerson, guard.Person},
		{guard.EntryMessengerAgent, guard.Agent},
		{guard.EntryMessengerAutomation, guard.Automation},
		{guard.EntryMessengerSystem, guard.System},
		{guard.EntryMessengerParticipant, guard.ExternalParticipant},
	} {
		ref := ledger.MessengerRef{Platform: "discord", Account: "account-1", Chat: "dm-1", Message: "msg"}
		in := ledger.Input{ID: "entry-" + string(rune('a'+i)), ConversationID: "messenger:discord:account-1:dm-1",
			AgentID:   "messenger:discord:account-1",
			Surface:   ledger.Surface{Kind: "messenger", Platform: ref.Platform, Account: ref.Account, ChatID: ref.Chat},
			Messenger: &ref, OccurredAt: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC),
			RecordedAt: time.Date(2026, 10, 24, 9, 0, 1, 0, time.UTC), TimeQuality: "recorded",
			Locator: "messenger:discord:account-1:dm-1:msg",
			// Text that claims another author changes nothing.
			Body: `speaker_kind=person author=owner "the person wrote this"`}
		if tc.kind == guard.Person {
			in.PersonID = "person"
		}
		e := admit(t, s, tc.entry, in)
		if e.SpeakerKind != tc.kind {
			t.Fatalf("entry %v: kind %q", tc.entry, e.SpeakerKind)
		}
	}
	// A missing or mismatched reference and surface are refused.
	for name, mutate := range map[string]func(*ledger.Input){
		"no reference":     func(in *ledger.Input) { in.Messenger = nil },
		"other platform":   func(in *ledger.Input) { in.Surface.Platform = "slack" },
		"no surface kind":  func(in *ledger.Input) { in.Surface.Kind = "tui" },
		"missing chat":     func(in *ledger.Input) { in.Surface.ChatID = "" },
		"agent surface id": func(in *ledger.Input) { in.Surface.Account = "" },
	} {
		in := messengerInput("invalid-" + name)
		mutate(&in)
		if _, err := s.Admit(t.Context(), guard.EntryMessengerPerson, in); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// A non-messenger entry cannot claim a messenger record.
	in := messengerInput("wrong-entry")
	if _, err := s.Admit(t.Context(), guard.EntryOwner, in); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("owner entry accepted a messenger record: %v", err)
	}
}

func TestMessengerRecordMasksURLSecretsAtAdmission(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	s := ledger.New(db, nil)
	hash := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	in := messengerInput("record-link")
	in.PersonID = ""
	in.Body = "call https://jun:synthetic-pass@example.com/join?token=synthetic-abc&page=2 and https://example.com/items/" + hash
	e := admit(t, s, guard.EntryMessengerParticipant, in)
	want := "call https://jun:" + secret.Placeholder + "@example.com/join?token=" + secret.Placeholder + "&page=2 and https://example.com/items/" + hash
	if e.Body.Text() != want {
		t.Fatalf("masked body = %q", e.Body.Text())
	}
	if e.Body.DerivedText() == e.Body.Text() || len(e.Spans) != 2 {
		t.Fatalf("secret spans: %+v", e.Spans)
	}
}
