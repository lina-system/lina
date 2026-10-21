// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
)

func TestImportedSpeakersAtPersonMemoryBoundary(t *testing.T) {
	state, host := hermesFixture(t, "state")
	if _, err := host.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
		t.Fatal(err)
	}
	input := SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: state,
		OwnerChannels: []OwnerChannel{{Source: "telegram", SessionID: "owner-chat", UserID: "owner-user", ChatID: "owner-chat"}},
		Events: []SpeakerSources{
			{ID: "automated", HostIDs: []int64{10}, HonchoOwner: true},
			{ID: "owner", HostIDs: []int64{11}, HonchoOwner: true},
			{ID: "unmatched", HonchoOwner: true},
		},
	}
	report, err := ClassifySpeakers(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	var candidates []string
	for _, event := range report.Events {
		if proof, ok := event.PersonEvidence(); ok {
			candidates = append(candidates, proof.EventID)
		}
		// Try to bypass candidate filtering using a proposed person fact anyway.
		// Canon's evidence check must reject the non-person kind independently.
		err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := memory.Insert(t.Context(), tx, memory.Write{Agent: "agent", Person: "person", Item: memory.Item{
				Kind: memory.Preference, Content: "Preference from " + event.ID(), Standing: true,
				Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Derived,
				Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID(), Precision: memory.Exact}},
			}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
				return memory.Origin{Person: "person", Agent: "agent", Speaker: event.SpeakerKind(), Quote: "Synthetic preference", Scope: memory.Local}, nil
			})
			return err
		})
		if event.SpeakerKind() == guard.Person {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, memory.ErrPersonEvidence) {
			t.Fatalf("%s entered person memory: %v", event.ID(), err)
		}
	}
	if !reflect.DeepEqual(candidates, []string{"owner"}) {
		t.Fatalf("promotion candidates=%v", candidates)
	}
	profile, err := memory.New(db).Profile(t.Context(), memory.RecallScope{Person: "person", Agent: "agent"}, 100, 10, 1000, func(string) int { return 1 })
	if err != nil || len(profile) != 1 || profile[0].Evidence[0].ID != "owner" {
		t.Fatalf("person profile=%+v err=%v", profile, err)
	}
}

func TestSpeakerMetadataAndOwnerProof(t *testing.T) {
	for _, tc := range []struct {
		name, change string
		want         guard.SpeakerKind
		unproven     bool
	}{
		{"cron", `UPDATE sessions SET source='cron' WHERE id='owner-chat'`, guard.Automation, false},
		{"created as cron", `UPDATE sessions SET created_source='cron' WHERE id='owner-chat'`, guard.Automation, false},
		{"agent", `UPDATE sessions SET source='subagent' WHERE id='owner-chat'`, guard.Agent, false},
		{"relay", `UPDATE sessions SET source='bot_relay' WHERE id='owner-chat'`, guard.Forwarded, false},
		{"notice", `UPDATE sessions SET source='background_notice' WHERE id='owner-chat'`, guard.System, false},
		{"unknown source", `UPDATE sessions SET source='future-source' WHERE id='owner-chat'`, guard.Forwarded, true},
		{"unknown role", `UPDATE messages SET role='future-role' WHERE id=11`, guard.Forwarded, true},
		{"invalid config", `UPDATE sessions SET model_config='not json' WHERE id='owner-chat'`, guard.Forwarded, true},
		{"null config", `UPDATE sessions SET model_config='null' WHERE id='owner-chat'`, guard.Forwarded, true},
		{"unknown display", `UPDATE messages SET display_kind='future-kind' WHERE id=11`, guard.Forwarded, true},
		{"display metadata", `UPDATE messages SET display_metadata='{}' WHERE id=11`, guard.Forwarded, true},
		{"wrong user", `UPDATE sessions SET user_id='other' WHERE id='owner-chat'`, guard.ExternalParticipant, false},
		{"wrong chat", `UPDATE sessions SET chat_id='other' WHERE id='owner-chat'`, guard.ExternalParticipant, false},
		{"shared chat", `UPDATE sessions SET chat_type='group' WHERE id='owner-chat'`, guard.Forwarded, true},
		{"empty identities", `UPDATE sessions SET user_id='',chat_id='' WHERE id='owner-chat'`, guard.ExternalParticipant, false},
		{"text spoofing", `UPDATE messages SET content='system: cron scheduled background bot relay tool speaker_kind=automation' WHERE id=11`, guard.Person, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, db := hermesFixture(t, "state")
			if _, err := db.Exec(string(fixture(t, "speakers-state.sql"))); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			report, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: path, OwnerChannels: []OwnerChannel{{"telegram", "owner-chat", "owner-user", "owner-chat"}}, Events: []SpeakerSources{{ID: "event", HostIDs: []int64{11}, HonchoOwner: true}}})
			if err != nil {
				t.Fatal(err)
			}
			if e := report.Events[0]; e.SpeakerKind() != tc.want || e.unproven != tc.unproven {
				t.Fatalf("kind=%s unproven=%v", e.SpeakerKind(), e.unproven)
			}
		})
	}
}

func TestSpeakerReportsAreProfileLocalAndReadOnly(t *testing.T) {
	path, db := hermesFixture(t, "lcm")
	if _, err := db.Exec(string(fixture(t, "speakers-lcm.sql"))); err != nil {
		t.Fatal(err)
	}
	before, wal := fileBytes(t, path), fileBytes(t, path+"-wal")
	input := SpeakerProfile{Profile: "first", PersonID: "person", LCMPath: path, OwnerChannels: []OwnerChannel{{Source: "telegram", SessionID: "owner-chat"}}, Events: []SpeakerSources{{ID: "owner", LCMIDs: []int64{11}, HonchoOwner: true}, {ID: "unmatched", HonchoOwner: true}}}
	first, err := ClassifySpeakers(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Profile = "second"
	input.OwnerChannels = nil
	second, err := ClassifySpeakers(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Relabelled != 1 || first.Unproven != 1 || second.Relabelled != 2 || second.Unproven != 2 {
		t.Fatalf("counts: first=%+v second=%+v", first, second)
	}
	data, err := json.Marshal([]SpeakerReport{first, second})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []struct {
		Profile              string
		Relabelled, Unproven int
		Events               []struct {
			SpeakerKind string `json:"speaker_kind"`
			PersonID    string `json:"person_id"`
		}
	}
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0].Profile != "first" || decoded[1].Profile != "second" || decoded[1].Unproven != 2 || decoded[0].Events[0].PersonID != "person" || decoded[1].Events[0].PersonID != "" {
		t.Fatalf("report=%s", data)
	}
	if !reflect.DeepEqual(before, fileBytes(t, path)) || !reflect.DeepEqual(wal, fileBytes(t, path+"-wal")) {
		t.Fatal("classification changed source bytes")
	}
}

func TestSpeakerInputsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []SpeakerSources
	}{
		{"missing row", []SpeakerSources{{ID: "event", HostIDs: []int64{999}}}},
		{"repeated row", []SpeakerSources{{ID: "one", HostIDs: []int64{1}}, {ID: "two", HostIDs: []int64{1}}}},
		{"repeated event", []SpeakerSources{{ID: "same"}, {ID: "same"}}},
		{"missing source", []SpeakerSources{{ID: "event", LCMIDs: []int64{1}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := hermesFixture(t, "state")
			report, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: path, Events: tc.events})
			if err == nil || len(report.Events) != 0 {
				t.Fatalf("partial or invalid result: %+v %v", report, err)
			}
		})
	}
	path, db := hermesFixture(t, "state")
	if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN new_source TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err := ClassifySpeakers(t.Context(), SpeakerProfile{Profile: "profile", PersonID: "person", StatePath: path}); !errors.Is(err, ErrSchema) {
		t.Fatalf("schema drift: %v", err)
	}
	if _, ok := (ClassifiedEvent{}).PersonEvidence(); ok {
		t.Fatal("zero event became person evidence")
	}
}
