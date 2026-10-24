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
	case forget, restore:
		return nil
	case erase:
		// The spans index positions in the text that is going away.
		if _, err := tx.ExecContext(ctx, "DELETE FROM ev_span WHERE event_id=?", target.ID); err != nil {
			return err
		}
		// The body column stays non-null: an empty text is no content, and the
		// check constraint needs one of text or material.
		_, err := tx.ExecContext(ctx, "UPDATE conv_event SET body='',material_id=NULL,material_revision=NULL WHERE id=?", target.ID)
		return err
	case disconnect:
		return ErrInvalid
	default:
		return ErrInvalid
	}
}

// expandEvents passes event targets through. The caller lists every revision of
// a record, because only it knows the record's revision chain.
func expandEvents(_ context.Context, _ *canon.Tx, e entry, targets []Target) ([]Target, error) {
	return targets, nil
}
