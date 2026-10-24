// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/design"
)

// eventLanguage is the language recorded with an admitted event. Reading a
// stored event never consults the process locale, so restart recovery keeps the
// language chosen at admission. An event admitted before Core recorded it falls
// back to its own script and then English.
func eventLanguage(event ledger.Event) design.Language {
	if event.Surface.Language != "" {
		return design.Language(event.Surface.Language)
	}
	return design.ConversationLanguage(event.Body.Text(), design.English)
}

func (r *conversationRuntime) inputLanguage(ctx context.Context, text string) (design.Language, error) {
	fallback := design.EnvironmentLanguage(os.Getenv)
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var language, body string
		err := tx.QueryRowContext(ctx, `SELECT coalesce(json_extract(surface,'$.language'),''),coalesce(body,'') FROM conv_event WHERE conversation_id=? AND speaker_kind='person' ORDER BY rowid DESC LIMIT 1`, r.conversation).Scan(&language, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if language != "" {
			fallback = design.Language(language)
		} else {
			fallback = design.ConversationLanguage(body, fallback)
		}
		return nil
	})
	return design.ConversationLanguage(text, fallback), err
}

func (r *conversationRuntime) turnLanguage(ctx context.Context, turnID string) (design.Language, error) {
	var input string
	err := r.db.Read(context.WithoutCancel(ctx), func(tx *canon.Tx) error {
		return tx.QueryRowContext(context.WithoutCancel(ctx), "SELECT input_event FROM turn_control WHERE turn_id=?", turnID).Scan(&input)
	})
	if errors.Is(err, sql.ErrNoRows) {
		// A turn with no durable input record has no admitted language. Use a
		// fixed default so restart recovery never adopts the process locale.
		return design.English, nil
	}
	if err != nil {
		return "", err
	}
	event, err := r.journal().Get(context.WithoutCancel(ctx), input)
	return eventLanguage(event), err
}
