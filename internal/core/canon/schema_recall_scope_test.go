// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"database/sql"
	"strings"
	"testing"
)

func TestRecallPersonScopeRewritesOnlyWhenItChanges(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	execRecall(t, db, `INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay) VALUES
 ('event','chat-agent','0','','agent','chat','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('summary','chat-sum','1','','agent','chat','2026-01-02T00:00:00Z','system','{"kind":"native"}',0),
 ('event','other-agent','0','kept','agent','other','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('memory','mem','1','owner','all','','2026-01-02T00:00:00Z','','{"kind":"native"}',0)`)

	var indexSQL string
	if err := db.Read(t.Context(), func(tx *Tx) error {
		err := tx.QueryRowContext(t.Context(), "SELECT coalesce(sql,'') FROM sqlite_schema WHERE type='index' AND name='recall_conversation'").Scan(&indexSQL)
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexSQL, "conversation") || !strings.Contains(indexSQL, "person") {
		t.Errorf("conversation lookup index = %q", indexSQL)
	}

	if writes := personRewrites(t, db, `INSERT INTO conv_event(id,conversation_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES('chat-agent-event','chat','agent','agent','{}','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z','observed','{"kind":"native"}','agent')`); writes != 0 {
		t.Errorf("agent event rewrote %d documents", writes)
	}
	expectRecallPersons(t, db, map[string]string{"chat-agent": "", "chat-sum": "", "other-agent": "kept", "mem": "owner"})

	if writes := personRewrites(t, db, personEventSQL, "chat-owner", "chat", "owner"); writes != 2 {
		t.Errorf("first owner event rewrote %d documents, want 2", writes)
	}
	expectRecallPersons(t, db, map[string]string{"chat-agent": "owner", "chat-sum": "owner", "other-agent": "kept", "mem": "owner"})
	expectViewPerson(t, db, "chat-owner", "owner")
	expectPending(t, db, "chat-owner")

	if writes := personRewrites(t, db, personEventSQL, "chat-owner-2", "chat", "owner"); writes != 0 {
		t.Errorf("repeat owner event rewrote %d documents", writes)
	}
	expectRecallPersons(t, db, map[string]string{"chat-agent": "owner", "chat-sum": "owner", "other-agent": "kept", "mem": "owner"})
	expectViewPerson(t, db, "chat-owner-2", "owner")

	if writes := personRewrites(t, db, personEventSQL, "chat-stranger", "chat", "stranger"); writes != 2 {
		t.Errorf("second person rewrote %d documents, want 2", writes)
	}
	expectRecallPersons(t, db, map[string]string{"chat-agent": "", "chat-sum": "", "other-agent": "kept", "mem": "owner"})
	expectViewPerson(t, db, "chat-stranger", "")

	if writes := personRewrites(t, db, personEventSQL, "chat-stranger-2", "chat", "stranger"); writes != 0 {
		t.Errorf("later mixed-conversation event rewrote %d documents", writes)
	}
	expectRecallPersons(t, db, map[string]string{"chat-agent": "", "chat-sum": "", "other-agent": "kept", "mem": "owner"})
	expectViewPerson(t, db, "chat-owner", "")

	if writes := personRewrites(t, db, personEventSQL, "other-owner", "other", "kept"); writes != 0 {
		t.Errorf("event matching the stored person rewrote %d documents", writes)
	}
	expectRecallPersons(t, db, map[string]string{"other-agent": "kept", "chat-agent": "", "mem": "owner"})

	if writes := personRewrites(t, db, personEventSQL, "other-next", "other", "next"); writes != 1 {
		t.Errorf("new person on the other conversation rewrote %d documents, want 1", writes)
	}
	expectRecallPersons(t, db, map[string]string{"other-agent": "", "chat-agent": "", "chat-sum": "", "mem": "owner"})
	expectViewPerson(t, db, "other-next", "")

	var plan string
	if err := db.Read(t.Context(), func(tx *Tx) error {
		rows, err := tx.QueryContext(t.Context(), "EXPLAIN QUERY PLAN UPDATE recall_doc SET person='x' WHERE conversation='chat' AND person<>'x'")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				return err
			}
			plan += detail + "\n"
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "INDEX recall_conversation") || strings.Contains(plan, "SCAN recall_doc") {
		t.Errorf("conversation update plan:\n%s", plan)
	}
}

func TestRecallPersonScopeUpgradeKeepsInferredPerson(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	var prior []Migration
	for i, migration := range all {
		if strings.Contains(migration.SQL, "CREATE TABLE recall_conversation_scope") {
			prior = all[:i]
			break
		}
	}
	if prior == nil {
		t.Fatal("recall conversation scope migration is missing")
	}
	cfg.Migrations = prior
	db := openStore(t, cfg)
	execRecall(t, db, `INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay) VALUES
 ('event','owned','0','','agent','owned','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('event','owned-stale','0','','agent','owned','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('event','mixed','0','','agent','mixed','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('event','quiet','0','','agent','quiet','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0),
 ('memory','mem','1','owner','all','','2026-01-02T00:00:00Z','','{"kind":"native"}',0)`)
	execRecall(t, db, personEventSQL, "owned-person", "owned", "owner")
	execRecall(t, db, personEventSQL, "mixed-owner", "mixed", "owner")
	execRecall(t, db, personEventSQL, "mixed-other", "mixed", "other")
	// Indexed after the person event, so the published trigger never repaired it.
	execRecall(t, db, `INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay)
 VALUES('event','owned-late','0','','agent','owned','2026-01-02T00:00:00Z','agent','{"kind":"native"}',0)`)
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	cfg.Migrations = all
	db = openStore(t, cfg)
	expectRecallPersons(t, db, map[string]string{
		"owned": "owner", "owned-stale": "owner", "owned-late": "owner",
		"mixed": "", "quiet": "", "mem": "owner",
	})
	var scopeOwner, scopeMixed, scopeQuiet int
	if err := db.Read(t.Context(), func(tx *Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_conversation_scope WHERE conversation='owned' AND person='owner'").Scan(&scopeOwner); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_conversation_scope WHERE conversation='mixed' AND person=''").Scan(&scopeMixed); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_conversation_scope WHERE conversation='quiet'").Scan(&scopeQuiet)
	}); err != nil {
		t.Fatal(err)
	}
	if scopeOwner != 1 || scopeMixed != 1 || scopeQuiet != 0 {
		t.Fatalf("backfill owned=%d mixed=%d quiet=%d", scopeOwner, scopeMixed, scopeQuiet)
	}
	if writes := personRewrites(t, db, personEventSQL, "owned-again", "owned", "owner"); writes != 0 {
		t.Errorf("upgraded repeat owner event rewrote %d documents", writes)
	}
	if writes := personRewrites(t, db, personEventSQL, "mixed-again", "mixed", "owner"); writes != 0 {
		t.Errorf("upgraded mixed conversation rewrote %d documents", writes)
	}
	if writes := personRewrites(t, db, personEventSQL, "quiet-owner", "quiet", "owner"); writes != 1 {
		t.Errorf("first person after upgrade rewrote %d documents, want 1", writes)
	}
	expectRecallPersons(t, db, map[string]string{"quiet": "owner", "owned-late": "owner", "mixed": "", "mem": "owner"})
}

const personEventSQL = `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
 VALUES(?,?,?,'agent','person','{}','2026-01-02T00:00:01Z','2026-01-02T00:00:01Z','observed','{"kind":"native"}','hello')`

func execRecall(t *testing.T, db *Store, query string, args ...any) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), query, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func personRewrites(t *testing.T, db *Store, query string, args ...any) int {
	t.Helper()
	var n int
	err := db.Write(t.Context(), func(tx *Tx) error {
		if _, err := tx.ExecContext(t.Context(), `CREATE TEMP TABLE IF NOT EXISTS person_writes(id INTEGER);
DELETE FROM person_writes;
CREATE TEMP TRIGGER IF NOT EXISTS count_person_write BEFORE UPDATE OF person ON main.recall_doc
BEGIN INSERT INTO person_writes VALUES(NEW.id); END;`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), query, args...); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM person_writes").Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func expectRecallPersons(t *testing.T, db *Store, want map[string]string) {
	t.Helper()
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for id, person := range want {
			var got string
			if err := tx.QueryRowContext(t.Context(), "SELECT person FROM recall_doc WHERE source_id=?", id).Scan(&got); err != nil {
				return err
			}
			if got != person {
				t.Errorf("%s person=%q, want %q", id, got, person)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func expectViewPerson(t *testing.T, db *Store, eventID, want string) {
	t.Helper()
	var got string
	if err := db.Read(t.Context(), func(tx *Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT person FROM recall_source WHERE kind='event' AND source_id=?", eventID).Scan(&got)
	}); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("view person for %s=%q, want %q", eventID, got, want)
	}
}

func expectPending(t *testing.T, db *Store, eventID string) {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM recall_pending WHERE kind='event' AND source_id=?", eventID).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pending %s=%d", eventID, n)
	}
}
