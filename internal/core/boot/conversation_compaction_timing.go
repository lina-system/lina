// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/kit/provider"
)

// lastRequestTime is when this conversation last sent a request. A request
// evidence event is the durable record of one; a conversation with none has no
// cache to lose.
func (r *conversationRuntime) lastRequestTime(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	var text string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT occurred_at FROM conv_event WHERE conversation_id=? AND scaffold_kind='request_assembly' ORDER BY rowid DESC LIMIT 1`, r.conversation).Scan(&text)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		at, err = time.Parse(time.RFC3339Nano, text)
		return err
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return at, text != "", nil
}

// cacheCold reports whether this turn boundary may compact before the trigger
// because the conversation's cache has expired. An unknown lifetime never
// counts as expired: the rewrite would cost the cache it cannot measure.
func (r *conversationRuntime) cacheCold(ctx context.Context, model provider.ModelRef, now time.Time) (bool, error) {
	supported, admitted := r.route.Capabilities(model)
	last, recorded, err := r.lastRequestTime(ctx)
	if err != nil {
		return false, err
	}
	return compaction.CacheCold(supported, admitted, last, recorded, now), nil
}
