// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
)

// SetEmbedding is an explicit person action, never a model tool. Chat switches
// further narrow this source consent. Revocation removes vectors atomically.
func (s *Store) SetEmbedding(ctx context.Context, person, platform, account string, enabled bool) error {
	if !validName(person) || !validName(platform) || !validName(account) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var present int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM messenger_source WHERE person=? AND platform=? AND account=?", person, platform, account).Scan(&present); err != nil {
			return err
		}
		if present != 1 {
			return ErrNotFound
		}
		if enabled {
			_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO messenger_embed_source(person,platform,account) VALUES(?,?,?)", person, platform, account)
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM messenger_embed_source WHERE person=? AND platform=? AND account=?", person, platform, account)
		return err
	})
}
