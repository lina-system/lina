// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/guard"
)

func TestImportedSpeakerAttribution(t *testing.T) {
	state, db := hermesFixture(t, "state")
	if _, err := db.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
		t.Fatal(err)
	}
	lcm, lc := hermesFixture(t, "lcm")
	if _, err := lc.Exec(string(fixture(t, "speakers-lcm.sql"))); err != nil {
		t.Fatal(err)
	}
	input := SpeakerProfile{
		Profile: "synthetic-profile", PersonID: "person", StatePath: state, LCMPath: lcm,
		OwnerChannels: []OwnerChannel{{Source: "telegram", SessionID: "owner-chat", UserID: "owner-user", ChatID: "owner-chat"}},
		Events:        honchoSpeakerFixture(t),
	}
	report, err := ClassifySpeakers(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := []guard.SpeakerKind{guard.Automation, guard.Person, guard.Forwarded, guard.Agent, guard.Tool, guard.System, guard.Agent, guard.Agent, guard.ExternalParticipant, guard.Automation, guard.Forwarded}
	if len(report.Events) != len(want) {
		t.Fatalf("events = %d", len(report.Events))
	}
	for i, e := range report.Events {
		if e.SpeakerKind() != want[i] {
			t.Errorf("%s: speaker=%s, want %s", e.ID(), e.SpeakerKind(), want[i])
		}
		person, ok := e.PersonEvidence()
		if ok != (want[i] == guard.Person) {
			t.Errorf("%s: person evidence eligibility=%v", e.ID(), ok)
		}
		if ok && person.PersonID != "person" {
			t.Errorf("owner identity missing: %+v", person)
		}
	}
	if report.Profile != "synthetic-profile" || report.Relabelled != 10 || report.Unproven != 2 {
		t.Errorf("counts: profile=%s relabelled=%d unproven=%d", report.Profile, report.Relabelled, report.Unproven)
	}
}

// A known automated source remains decisive when another copy has incomplete
// provenance. Honcho's owner claim must not turn either copy into person.
func TestAutomatedHonchoOwnerWithIncompleteCopy(t *testing.T) {
	state, db := hermesFixture(t, "state")
	if _, err := db.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET created_source='unrecognized-origin' WHERE id='scheduled'`); err != nil {
		t.Fatal(err)
	}
	report, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: state, Events: []SpeakerSources{{ID: "automated-owner-message", HostIDs: []int64{10}, HonchoOwner: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if e := report.Events[0]; e.SpeakerKind() != guard.Automation {
		t.Fatalf("automated prompt stored as Honcho owner: speaker=%s, want automation", e.SpeakerKind())
	}
}

func honchoSpeakerFixture(t *testing.T) []SpeakerSources {
	t.Helper()
	var claims []struct {
		ID, Peer, Content string
		HostIDs           []int64 `json:"host_ids"`
		LCMIDs            []int64 `json:"lcm_ids"`
	}
	if err := json.Unmarshal(fixture(t, "speakers-honcho.json"), &claims); err != nil {
		t.Fatal(err)
	}
	events := make([]SpeakerSources, 0, len(claims))
	for _, c := range claims {
		events = append(events, SpeakerSources{ID: c.ID, HostIDs: c.HostIDs, LCMIDs: c.LCMIDs, HonchoOwner: c.Peer == "owner"})
	}
	return events
}
