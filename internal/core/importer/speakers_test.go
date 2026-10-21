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

// LCM can retain a message whose host message row is absent. Its session still
// carries provenance in the host database and must participate in attribution.
func TestLCMOnlyHonchoOwnerUsesHostSessionKind(t *testing.T) {
	for _, tc := range []struct {
		name, update string
		want         guard.SpeakerKind
		unproven     int
	}{
		{"delegated", `UPDATE sessions SET model_config='{"_delegate_from":"synthetic-parent"}' WHERE id='owner-chat'`, guard.Agent, 0},
		{"cron", `UPDATE sessions SET source='cron' WHERE id='owner-chat'`, guard.Automation, 0},
		{"created by cron", `UPDATE sessions SET created_source='cron' WHERE id='owner-chat'`, guard.Automation, 0},
		{"subagent", `UPDATE sessions SET source='subagent' WHERE id='owner-chat'`, guard.Agent, 0},
		{"shared chat", `UPDATE sessions SET chat_type='group' WHERE id='owner-chat'`, guard.Forwarded, 1},
		{"owner", `UPDATE sessions SET model_config='{}' WHERE id='owner-chat'`, guard.Person, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, host := hermesFixture(t, "state")
			if _, err := host.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
				t.Fatal(err)
			}
			if _, err := host.Exec(`DELETE FROM messages WHERE session_id='owner-chat'`); err != nil {
				t.Fatal(err)
			}
			if _, err := host.Exec(tc.update); err != nil {
				t.Fatal(err)
			}
			lcm, db := hermesFixture(t, "lcm")
			if _, err := db.Exec(string(fixture(t, "speakers-lcm.sql"))); err != nil {
				t.Fatal(err)
			}
			report, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: state, LCMPath: lcm,
				OwnerChannels: []OwnerChannel{{"telegram", "owner-chat", "owner-user", "owner-chat"}},
				Events:        []SpeakerSources{{ID: "lcm-only-owner-claim", LCMIDs: []int64{11}, HonchoOwner: true}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Events[0].SpeakerKind(); got != tc.want {
				t.Fatalf("Honcho owner claim matched only to LCM: speaker=%s, want %s", got, tc.want)
			}
			if _, ok := report.Events[0].PersonEvidence(); ok != (tc.want == guard.Person) {
				t.Fatal("incorrect person evidence eligibility")
			}
			relabelled := 1
			if tc.want == guard.Person {
				relabelled = 0
			}
			if report.Relabelled != relabelled || report.Unproven != tc.unproven {
				t.Fatalf("counts: relabelled=%d unproven=%d", report.Relabelled, report.Unproven)
			}
		})
	}
}
