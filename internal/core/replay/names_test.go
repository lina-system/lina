// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/replay"
)

func TestSharedNameReplayEvidenceAuthorization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sql string
		person    string
		allowed   bool
	}{
		{name: "shared evidence", allowed: true},
		{name: "foreign person", person: "other"},
		{name: "personal holder", sql: `UPDATE memory_item SET holder='unrelated'`},
		{name: "private name", sql: `UPDATE memory_item SET scope='private'`},
		{name: "never leaves", sql: `UPDATE memory_item SET scope='never-leaves'`},
		{name: "future name", sql: `UPDATE memory_item SET valid_from=200`},
		{name: "expired name", sql: `UPDATE memory_item SET valid_until=50`},
		{name: "secret name", sql: `UPDATE memory_item SET record=json_set(record,'$.sensitivity',json('["secret"]'))`},
		{name: "changed revision", sql: `UPDATE memory_item SET record=json_set(record,'$.revision',3)`},
		{name: "forgotten name", sql: `UPDATE memory_item SET state='retracted'`},
		{name: "forgotten source", sql: `INSERT INTO retract_block(item_id,person,holder,event_id,conversation_id) VALUES('removed','person','A','source-A','')`},
		{name: "forgotten conversation", sql: `INSERT INTO retract_block(item_id,person,holder,event_id,conversation_id) VALUES('removed','person','A','','chat-A')`},
		{name: "private source", sql: `UPDATE memory_item SET scope='private' WHERE json_extract(record,'$.kind')='fact'`},
		{name: "never leaves source", sql: `UPDATE memory_item SET scope='never-leaves' WHERE json_extract(record,'$.kind')='fact'`},
		{name: "mixed person conversation", sql: `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body) VALUES('other-input','chat-A','other','A','person','{"kind":"tui"}','1970-01-01T00:00:12Z','1970-01-01T00:00:12Z','observed','{"kind":"native"}','other person')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, source, _ := fixture(t)
			flow := coreexec.NewMemory(db, nil, nil)
			var receipt coreexec.RememberReceipt
			for i, agent := range []string{"A", "B"} {
				at := time.Unix(int64(10+i), 0)
				_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source-" + agent, ConversationID: "chat-" + agent, AgentID: agent, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Source-only words for " + agent})
				if err != nil {
					t.Fatal(err)
				}
				in := memory.Write{Agent: agent, Person: "person", Item: memory.Item{Kind: memory.Name, Subject: memory.Subject{Kind: memory.World, ID: "computer"}, Content: "Name " + agent, Name: &memory.NameValue{Current: "Name " + agent}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "source-" + agent, Precision: memory.Exact}}}}
				receipt, err = flow.Remember(t.Context(), in)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					in.Item.Kind, in.Item.Name = memory.Fact, nil
					if _, err := flow.Remember(t.Context(), in); err != nil {
						t.Fatal(err)
					}
				}
			}
			if receipt.Revision != 2 {
				t.Fatal("fixture did not accumulate both agents' evidence")
			}
			if tc.sql != "" {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), tc.sql)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			epoch, err := source.Epoch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			person := tc.person
			if person == "" {
				person = "person"
			}
			for _, agent := range []string{"A", "B", "C"} {
				ep := replay.Episode{RevocationEpoch: epoch, Evidence: ledger.RequestEvidence{Assembly: ledger.Assembly{InstructionFormat: "persona-v1", PreparedAt: time.Unix(100, 0).UTC().Format(time.RFC3339Nano), OwnerPersonID: person, AgentID: agent, Context: []ledger.ContextSource{{Reference: ledger.Reference{ID: "memory:" + receipt.ID, Revision: "2"}, Source: "name-v1"}}}}}
				err := source.ValidateEpisode(t.Context(), ep)
				if (err == nil) != tc.allowed {
					t.Errorf("agent %s: allowed=%v, error=%v", agent, tc.allowed, err)
				}
			}
		})
	}
}

func TestCommonNameDoesNotAuthorizeForeignRawEvents(t *testing.T) {
	t.Parallel()
	db, source, _ := fixture(t)
	id := seed(t, db, "input", guard.EntryOwner)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	foreign, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "foreign", ConversationID: "foreign-chat", AgentID: "foreign-agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Foreign source-only words"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coreexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: foreign.AgentID, Person: "person", Item: memory.Item{Kind: memory.Name, Subject: memory.Subject{Kind: memory.World, ID: "computer"}, Content: "Birch", Name: &memory.NameValue{Current: "Birch"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: foreign.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	// An expand result is raw source text, not shared-name provenance.
	call := []byte(`{"id":"expand","name":"expand","arguments":"{}"}`)
	result, _ := json.Marshal(map[string]any{"id": "expand", "code": "ok", "output": map[string]any{"events": []map[string]string{{"id": foreign.ID, "text": foreign.Body.Text()}}}})
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,'expand',?,?)`, id, call, result)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id)
	if err != replay.Exclusion("restricted_content") {
		t.Fatalf("foreign raw event authorized by common name: %v", err)
	}
}
