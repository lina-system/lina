// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"strconv"
)

// A platform message id is meaningful only within its authenticated address.
func (c *channelRuntime) channelReplyEvent(ctx context.Context, address channels.Address, native string) (string, error) {
	if native == "" {
		return "", nil
	}
	var id string
	err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT o.ledger_ref FROM channel_outbound o,json_each(o.message_ids) m WHERE o.address=? AND m.value=? ORDER BY o.sequence DESC LIMIT 1`, address.Key(), native).Scan(&id)
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT i.id,i.body FROM channel_receipts r JOIN inbox i ON i.id=r.inbox_id JOIN conv_event e ON e.id='channel-input:'||i.id WHERE r.address=? ORDER BY i.id DESC`, address.Key())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var number int64
			var raw []byte
			if err = rows.Scan(&number, &raw); err != nil {
				return err
			}
			var event gateway.Inbound
			if err = json.Unmarshal(raw, &event); err != nil {
				return err
			}
			var body struct {
				ID string `json:"message_id"`
			}
			if err = json.Unmarshal(event.Body, &body); err != nil {
				return err
			}
			if body.ID == native {
				id = "channel-input:" + strconv.FormatInt(number, 10)
				return nil
			}
		}
		return rows.Err()
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
