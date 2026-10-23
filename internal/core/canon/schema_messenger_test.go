// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"
)

// Migration 63 adds the messenger source registry and widens the ledger's
// relation kinds. A published install keeps its events and relations.
func TestMessengerUpgradeKeepsLedgerRelationsAndAddsKinds(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append([]Migration{}, Migrations()[:61]...)
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
VALUES('older','conversation','person','agent','person','{"kind":"tui"}','2026-01-02T03:04:05Z','2026-01-02T03:04:06Z','observed','{"kind":"native"}','older body');
INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
VALUES('newer','conversation','person','agent','person','{"kind":"tui"}','2026-01-02T03:04:07Z','2026-01-02T03:04:08Z','observed','{"kind":"native"}','newer body');
INSERT INTO ev_rel(event_id,target_id,kind,ordinal) VALUES('newer','older','view_of',1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	defer db.Close(t.Context())
	if err := db.Write(t.Context(), func(tx *Tx) error {
		var kind string
		var ordinal int
		if err := tx.QueryRowContext(t.Context(), "SELECT kind,ordinal FROM ev_rel WHERE event_id='newer'").Scan(&kind, &ordinal); err != nil {
			return err
		}
		if kind != "view_of" || ordinal != 1 {
			return errors.New("relation changed")
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO ev_rel(event_id,target_id,kind) VALUES('newer','older','replies_to');
INSERT INTO ev_rel(event_id,target_id,kind) VALUES('newer','older','in_thread')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE ev_rel SET ordinal=2",
		"DELETE FROM ev_rel WHERE kind='replies_to'",
		"INSERT INTO ev_rel(event_id,target_id,kind) VALUES('newer','older','replies_to')",
		"INSERT INTO ev_rel(event_id,target_id,kind,ordinal) VALUES('newer','older','pairs_with',2)",
		"INSERT INTO ev_rel(event_id,target_id,kind) VALUES('newer','older','unrelated')",
	} {
		if err := db.Write(t.Context(), func(tx *Tx) error {
			_, err := tx.ExecContext(t.Context(), query)
			return err
		}); err == nil {
			t.Fatalf("relation change accepted: %s", query)
		}
	}
}

func TestMessengerTablesRequireSourceAndKeepUniqueReferences(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	insert := func(query string) error {
		return db.Write(t.Context(), func(tx *Tx) error {
			_, err := tx.ExecContext(t.Context(), query)
			return err
		})
	}
	if err := insert(`INSERT INTO messenger_source(person,platform,account,mode,adapter,state,connected_at)
VALUES('person','discord','account-1','official','discord','connected','2026-10-24T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO messenger_chat(platform,account,chat,kind,cursor) VALUES('discord','account-1','dm-1','direct','10')`); err != nil {
		t.Fatal(err)
	}
	// A chat without its source is refused.
	if err := insert(`INSERT INTO messenger_chat(platform,account,chat,kind) VALUES('discord','missing','dm-2','direct')`); err == nil {
		t.Fatal("chat accepted without its source")
	}
	// An unknown chat kind and an unreviewed source state are refused.
	if err := insert(`INSERT INTO messenger_chat(platform,account,chat,kind) VALUES('discord','account-1','dm-3','broadcast')`); err == nil {
		t.Fatal("unknown chat kind accepted")
	}
	if err := insert(`UPDATE messenger_source SET state='paused'`); err == nil {
		t.Fatal("unknown source state accepted")
	}
	record := `INSERT INTO messenger_record(platform,account,chat,message,event,occurred_at) VALUES('discord','account-1','dm-1','m-1','event-1','2026-10-24T00:00:00Z')`
	if err := insert(record); err != nil {
		t.Fatal(err)
	}
	if err := insert(record); err == nil {
		t.Fatal("duplicate legacy reference accepted")
	}
	if err := insert(`INSERT INTO messenger_record_revision(platform,account,chat,message,revision,event) VALUES('discord','account-1','dm-1','m-1',1,'event-1')`); err != nil {
		t.Fatal(err)
	}
	if err := insert(`INSERT INTO messenger_record_revision(platform,account,chat,message,revision,event) VALUES('discord','account-1','dm-1','m-1',2,'event-1')`); err == nil {
		t.Fatal("one event used for two revisions")
	}
}

// Hiding an event removes its document from the one index; restoring queues it.
func TestMessengerRemovalHidesTheIndexedDocument(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
VALUES('record','conversation','person','messenger:discord:account-1','person','{"kind":"messenger","platform":"discord","account":"account-1","chat_id":"dm-1"}','2026-10-24T00:00:00Z','2026-10-24T00:00:00Z','recorded','{"kind":"messenger"}','hello');
INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay)
VALUES('event','record','0','person','messenger:discord:account-1','messenger:discord:account-1:dm-1','2026-10-24T00:00:00Z','person','{"kind":"messenger"}',0);
INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'op-1','digest');
INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
VALUES('event','record','person','messenger:discord:account-1','messenger:discord:account-1','trashed',1,'{}','op-1')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.Read(t.Context(), func(tx *Tx) error {
			return tx.QueryRowContext(t.Context(), query).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT count(*) FROM recall_doc WHERE kind='event' AND source_id='record'"); n != 0 {
		t.Fatalf("hidden record stayed in the index: %d", n)
	}
	if n := count("SELECT count(*) FROM retract_visible_event WHERE id='record'"); n != 0 {
		t.Fatal("hidden record stayed visible")
	}
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE retract_target SET state='active' WHERE kind='event' AND id='record'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT count(*) FROM recall_pending WHERE kind='event' AND source_id='record'"); n != 1 {
		t.Fatalf("restored record was not queued: %d", n)
	}
	if n := count("SELECT count(*) FROM retract_visible_event WHERE id='record'"); n != 1 {
		t.Fatal("restored record stayed hidden")
	}
}
