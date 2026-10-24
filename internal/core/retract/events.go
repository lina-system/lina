// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Event targets are messenger records. Their identity and provenance come from
// canon: the conversation, the holder that keeps the record, and the platform
// reference the tombstone keeps. A native conversation's deletion has its own
// owner, so this coordinator refuses it.
func resolveEvent(ctx context.Context, tx *canon.Tx, ref Ref, person string) (Target, error) {
	if ref.Kind != Event || ref.ID == "" {
		return Target{}, ErrInvalid
	}
	var conversation, agent, origin, surface string
	err := tx.QueryRowContext(ctx, "SELECT conversation_id,agent_id,origin,surface FROM conv_event WHERE id=?", ref.ID).Scan(&conversation, &agent, &origin, &surface)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{}, ErrInvalid
	}
	if err != nil {
		return Target{}, err
	}
	var provenance struct {
		Kind      string `json:"kind"`
		Messenger *struct {
			Platform string `json:"platform"`
			Account  string `json:"account"`
			Chat     string `json:"chat"`
			Message  string `json:"message"`
		} `json:"messenger"`
	}
	if err := json.Unmarshal([]byte(origin), &provenance); err != nil {
		return Target{}, err
	}
	if provenance.Kind != "messenger" || provenance.Messenger == nil || agent == "" {
		return Target{}, ErrInvalid
	}
	ref0 := provenance.Messenger
	for _, field := range []string{ref0.Platform, ref0.Account, ref0.Chat, ref0.Message} {
		if field == "" {
			return Target{}, ErrInvalid
		}
	}
	return Target{
		Kind: Event, ID: ref.ID, Agent: agent, Holder: "",
		Conversations: []string{conversation},
		Legacy: []LegacyRef{{
			System:   "messenger",
			Instance: ref0.Platform + "/" + ref0.Account,
			Kind:     "message",
			Key:      ref0.Chat + "/" + ref0.Message,
		}},
	}, nil
}

// An event's text is a ledger record, which is never rewritten while the record
// lives. Hiding it through the retraction projection is what makes it invisible
// at once; its permanent deletion is the one rewrite: the text and the material
// reference go, and the row keeps the identity, the origin and the legacy
// reference the tombstone carries.
func applyEvent(ctx context.Context, tx *canon.Tx, e entry, target Target) error {
	if target.Kind != Event {
		return ErrInvalid
	}
	switch e.Action {
	case forget, erase:
		// Attachment names are part of the record. The removal list is what a
		// restored generation replays, so the names have to leave here rather
		// than in a write the journal cannot repeat.
		if err := dropMessengerAttachments(ctx, tx, target.ID); err != nil {
			return err
		}
		if e.Action == forget {
			return nil
		}
		// The spans index positions in the text that is going away.
		if _, err := tx.ExecContext(ctx, "DELETE FROM ev_span WHERE event_id=?", target.ID); err != nil {
			return err
		}
		// The body column stays non-null: an empty text is no content, and the
		// check constraint needs one of text or material.
		_, err := tx.ExecContext(ctx, "UPDATE conv_event SET body='',material_id=NULL,material_revision=NULL WHERE id=?", target.ID)
		return err
	case restore:
		return nil
	case disconnect:
		return ErrInvalid
	default:
		return ErrInvalid
	}
}

// dropMessengerAttachments removes every attachment row of the message that
// owns this event. A revision event identifies the message; older revisions of
// that message lose their names in the same removal.
func dropMessengerAttachments(ctx context.Context, tx *canon.Tx, event string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM messenger_attachment
WHERE EXISTS (
 SELECT 1 FROM messenger_record_revision r
 WHERE r.event=? AND r.platform=messenger_attachment.platform AND r.account=messenger_attachment.account
  AND r.chat=messenger_attachment.chat AND r.message=messenger_attachment.message
)`, event)
	return err
}

// expandEvents passes event targets through. The caller lists every revision of
// a record, because only it knows the record's revision chain.
func expandEvents(_ context.Context, _ *canon.Tx, e entry, targets []Target) ([]Target, error) {
	return targets, nil
}
