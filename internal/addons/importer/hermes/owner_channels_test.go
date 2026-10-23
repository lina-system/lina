// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/guard"
)

func TestOwnerDisplayProvenance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source, display, metadata, chatType string
		confirmed                                 bool
		want                                      guard.SpeakerKind
	}{
		{"gateway token", "telegram", "", `{"gateway_input_owner":"delivery-id"}`, "private", true, guard.Person},
		{"token without map", "telegram", "", `{"gateway_input_owner":"delivery-id"}`, "private", false, guard.ExternalParticipant},
		{"steer", "telegram", "steer", "", "private", true, guard.Person},
		{"steer token", "telegram", "steer", `{"gateway_input_owner":"delivery-id"}`, "private", true, guard.Person},
		{"notification", "telegram", "internal_notification", `{"gateway_input_owner":"delivery-id"}`, "private", true, guard.System},
		{"notification metadata", "telegram", "internal_notification", `{"display_text":"background result"}`, "private", true, guard.System},
		{"unknown key", "telegram", "", `{"gateway_input_owner":"delivery-id","future":true}`, "private", true, guard.Forwarded},
		{"invalid token", "telegram", "", `{"gateway_input_owner":42}`, "private", true, guard.Forwarded},
		{"empty token", "telegram", "", `{"gateway_input_owner":""}`, "private", true, guard.Forwarded},
		{"malformed metadata", "telegram", "", `{`, "private", true, guard.Forwarded},
		{"unknown kind", "telegram", "future", `{"gateway_input_owner":"delivery-id"}`, "private", true, guard.Forwarded},
		{"group token", "telegram", "", `{"gateway_input_owner":"delivery-id"}`, "group", true, guard.Forwarded},
		{"desktop", "desktop", "", "", "", true, guard.Person},
		{"desktop steer", "desktop", "steer", "", "", true, guard.Person},
		{"desktop notice", "desktop", "internal_notification", "", "", true, guard.System},
		{"webui", "webui", "", "", "", true, guard.Person},
		{"unconfirmed desktop", "desktop", "", "", "", false, guard.Forwarded},
		{"unconfirmed webui", "webui", "", "", "", false, guard.Forwarded},
		{"desktop group", "desktop", "", "", "group", true, guard.Forwarded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, db := hermesFixture(t, "state")
			if _, err := db.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
				t.Fatal(err)
			}
			user, chat := "owner-user", "owner-chat"
			if tc.source == "desktop" || tc.source == "webui" {
				user, chat = "", ""
			}
			if _, err := db.Exec(`UPDATE sessions SET source=?,created_source=?,user_id=?,chat_id=?,chat_type=? WHERE id='owner-chat'`, tc.source, tc.source, user, chat, tc.chatType); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE messages SET display_kind=?,display_metadata=? WHERE id=11`, tc.display, tc.metadata); err != nil {
				t.Fatal(err)
			}
			in := SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: path, Events: []SpeakerSources{{ID: "event", HostIDs: []int64{11}}}}
			if tc.confirmed {
				in.OwnerChannels = []OwnerChannel{{tc.source, "owner-chat", user, chat}}
			}
			report, err := ClassifySpeakers(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			if got := report.Events[0].SpeakerKind(); got != tc.want {
				t.Fatalf("speaker=%s, want %s", got, tc.want)
			}
			if _, ok := report.Events[0].PersonEvidence(); ok != (tc.want == guard.Person) {
				t.Fatal("incorrect evidence eligibility")
			}
		})
	}
}

func TestRehearsalOwnerCandidates(t *testing.T) {
	t.Parallel()
	path, db := hermesFixture(t, "state")
	if _, err := db.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,source,started_at) VALUES ('desktop-session','desktop',300),('webui-session','webui',300)`); err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(t.Context(), Request{BatchID: "batch", Confirmed: true, Rehearsal: true, Profiles: []Profile{{ID: "profile", AgentID: "agent", PersonID: "person", StatePath: path}}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	report, err := plan.Rehearse()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Mappings []struct {
			CandidateOwnerChannels []struct{ Source, SessionID, UserID, ChatID, ChatType string }
		}
	}
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	candidates := decoded.Mappings[0].CandidateOwnerChannels
	if len(candidates) != 6 {
		t.Fatalf("owner candidates=%+v, want six interactive host sessions", candidates)
	}
	found := false
	for _, c := range candidates {
		if c.SessionID == "owner-chat" {
			found = c.Source == "telegram" && c.UserID == "owner-user" && c.ChatID == "owner-chat" && c.ChatType == "private"
		}
	}
	if !found {
		t.Fatalf("Telegram provenance missing: %+v", candidates)
	}
}

func TestLocalOwnerRecordedIdentity(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"desktop", "webui"} {
		t.Run(source, func(t *testing.T) {
			path, db := hermesFixture(t, "state")
			if _, err := db.Exec(`UPDATE sessions SET source=?,user_id='basic:owner' WHERE id='synthetic-session'`, source); err != nil {
				t.Fatal(err)
			}
			for _, user := range []string{"basic:owner", "basic:other", ""} {
				report, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: path, OwnerChannels: []OwnerChannel{{Source: source, SessionID: "synthetic-session", UserID: user}}, Events: []SpeakerSources{{ID: "input", HostIDs: []int64{1}}}})
				if err != nil {
					t.Fatal(err)
				}
				want := guard.ExternalParticipant
				if user == "basic:owner" {
					want = guard.Person
				}
				if got := report.Events[0].SpeakerKind(); got != want {
					t.Fatalf("recorded identity %q: got %s want %s", user, got, want)
				}
			}
		})
	}
}
