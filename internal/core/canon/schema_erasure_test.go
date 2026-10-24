// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"strings"
	"testing"
)

// Migration 66 keeps the ledger append-only and allows only the rewrite a
// permanent deletion makes: clearing the text and the spans it indexed once the
// event is erased. Every other change still aborts.
func TestEventErasureAllowsOnlyTheClearingRewrite(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	defer db.Close(t.Context())
	seed := func() error {
		return db.Write(t.Context(), func(tx *Tx) error {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event(id,conversation_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,body)
VALUES('event-1','conversation','person','agent','person','{"kind":"tui"}','2026-01-02T03:04:05Z','2026-01-02T03:04:06Z','observed','{"kind":"native"}','kept text');
INSERT INTO ev_span(event_id,start,end,sensitivity) VALUES('event-1',0,9,'secret')`)
			return err
		})
	}
	if err := seed(); err != nil {
		t.Fatal(err)
	}
	expectAppendOnly := func(stage string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "ledger_append_only") {
			t.Fatalf("%s: got %v, want the append-only refusal", stage, err)
		}
	}
	expectAppendOnly("text update", db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE conv_event SET body='replaced' WHERE id='event-1'")
		return err
	}))
	expectAppendOnly("span delete", db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM ev_span WHERE event_id='event-1'")
		return err
	}))
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'removal-1','digest');
INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
VALUES('event','event-1','person','','agent','erased',1,'{}','removal-1')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A change outside the clearing rewrite still aborts, erased or not.
	expectAppendOnly("origin update", db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE conv_event SET origin='{\"kind\":\"messenger\"}',body='',material_id=NULL,material_revision=NULL WHERE id='event-1'")
		return err
	}))
	if err := db.Write(t.Context(), func(tx *Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DELETE FROM ev_span WHERE event_id='event-1'"); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "UPDATE conv_event SET body='',material_id=NULL,material_revision=NULL WHERE id='event-1'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var body, origin string
		var spans int
		if err := tx.QueryRowContext(t.Context(), "SELECT body,origin FROM conv_event WHERE id='event-1'").Scan(&body, &origin); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM ev_span WHERE event_id='event-1'").Scan(&spans); err != nil {
			return err
		}
		if body != "" || spans != 0 || !strings.Contains(origin, "native") {
			t.Fatalf("erasing changed more than the text: body=%q spans=%d origin=%s", body, spans, origin)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
