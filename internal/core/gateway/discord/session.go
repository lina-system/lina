// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
)

type frame struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
	Seq  *int64          `json:"s,omitempty"`
	Type string          `json:"t,omitempty"`
}
type cursor struct {
	Session string `json:"session"`
	Seq     int64  `json:"seq"`
	// Intents records what the saved session identified with. Discord rejects
	// a Resume that changes them, so a different set forces a fresh Identify.
	Intents Intents `json:"intents,omitempty"`
}

// sessionError maps a close to an outcome. Invalid sequence and expired
// sessions need a fresh Identify; other closes resume from the cursor.
func sessionError(err error) (reset bool, cause error) {
	switch websocket.CloseStatus(err) {
	case websocket.StatusCode(4013), websocket.StatusCode(4014):
		return false, ErrIntentsDenied
	case 4007, 4009:
		return true, errUnavailable
	}
	return false, errUnavailable
}

// Run joins the bot token's Gateway session and admits channel input from it.
// A messenger source on the same token shares the session.
func (a *Adapter) Run(ctx context.Context, in gateway.Admitter) error {
	if in == nil {
		return channels.ErrInvalid
	}
	if err := a.resumeButtons(ctx); err != nil {
		return err
	}
	content, err := MessageContent(ctx, a.db, a.bot)
	if err != nil {
		return err
	}
	intents := baseIntents
	if content {
		intents |= IntentMessageContent
	}
	return Listen(ctx, Listener{Bot: a.bot, Token: a.token, DB: a.db, Transport: a.transport, Intents: intents,
		Handle: func(ctx context.Context, kind string, data json.RawMessage, cursor string) error {
			return a.dispatch(ctx, in, kind, data, cursor)
		}})
}

// Message content is privileged on Discord. DMs and addressed guild messages
// work without that intent; request it only for an explicit all-messages binding.
func MessageContent(ctx context.Context, db channels.Canon, bot channels.Bot) (bool, error) {
	required := false
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT scope FROM channel_bindings WHERE bot=?", bot.Key())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			var scope channels.Scope
			if err := rows.Scan(&data); err != nil {
				return err
			}
			if err := json.Unmarshal(data, &scope); err != nil {
				return err
			}
			required = required || scope.Reads == channels.AllMessages
		}
		return rows.Err()
	})
	return required, err
}
