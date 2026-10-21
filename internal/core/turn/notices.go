// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// ControlSchema is a separate numbered migration after Schema and the ledger
// schema. The input event supplies durable conversation, agent and surface
// identity even when the process dies before a turn can finish.
const ControlSchema = `
CREATE TABLE turn_control (
 turn_id TEXT PRIMARY KEY NOT NULL REFERENCES turn_runs(id),
 input_event TEXT NOT NULL REFERENCES conv_event(id),
 issuer TEXT,
 notice_event TEXT UNIQUE REFERENCES conv_event(id)
);
`

// The ledger owns its tables. This adapter lets it append a short system event
// inside the same fenced transaction as the terminal turn state. It does not
// open a writer or commit independently.
type ledgerTransaction struct{ tx *canon.Tx }

func (t ledgerTransaction) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }
func (t ledgerTransaction) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }

func claimContext(ctx context.Context, tx *canon.Tx, id, conversation, eventID string) error {
	if eventID == "" {
		return nil
	}
	e, err := ledger.New(ledgerTransaction{tx}, nil).Get(ctx, eventID)
	if err != nil && !errors.Is(err, ledger.ErrBytesUnavailable) {
		return err
	}
	if e.ConversationID != conversation {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO turn_control(turn_id,input_event) VALUES(?,?)", id, eventID)
	return err
}

func interruptNotice(ctx context.Context, tx *canon.Tx, id string, cause Interrupt) error {
	return terminalNotice(ctx, tx, id, cause.Issuer, "interruption", cause.Text())
}

func failureNotice(ctx context.Context, tx *canon.Tx, id string) error {
	return terminalNotice(ctx, tx, id, nil, "failure", "The reply could not be completed. Send a new message to continue.")
}

func terminalNotice(ctx context.Context, tx *canon.Tx, id string, issuer any, kind, text string) error {
	var eventID string
	var notice sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT input_event,notice_event FROM turn_control WHERE turn_id=?", id).Scan(&eventID, &notice)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || notice.Valid {
		return err
	}
	events := ledger.New(ledgerTransaction{tx}, nil)
	source, err := events.Get(ctx, eventID)
	if err != nil && !errors.Is(err, ledger.ErrBytesUnavailable) {
		return err
	}
	digest := sha256.Sum256([]byte(id))
	noticeID := "turn-" + kind + "-" + hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	_, err = events.Admit(ctx, guard.EntrySystem, ledger.Input{
		ID: noticeID, ConversationID: source.ConversationID, ParentID: source.ID,
		AgentID: source.AgentID, Surface: source.Surface, OccurredAt: now, RecordedAt: now,
		TimeQuality: "recorded", Body: text, ScaffoldKind: kind,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE turn_control SET issuer=?,notice_event=? WHERE turn_id=?", issuer, noticeID, id)
	return err
}

func recoverNotices(ctx context.Context, tx *canon.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM turn_runs WHERE state='active' ORDER BY id")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = interruptNotice(ctx, tx, id, Interrupt{Issuer: EpochLost}); err != nil {
			return err
		}
	}
	return nil
}
