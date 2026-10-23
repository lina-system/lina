// SPDX-License-Identifier: Apache-2.0
package results

import (
	"context"
	"database/sql"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
)

// ReadRevision returns a retained revision after checking the current head's
// ownership. It cannot reveal another person's result through a ledger lookup.
func (s *Store) ReadRevision(ctx context.Context, scope result.Scope, id string, revision int) (result.Result, error) {
	var value result.Result
	if !validScope(scope) || id == "" || revision < 1 {
		return value, result.ErrMalformed
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		head, _, err := s.head(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if revision > head.Revision {
			return ErrUnavailable
		}
		var event string
		err = tx.QueryRowContext(ctx, `SELECT id FROM conv_event WHERE agent_id=? AND conversation_id=? AND scaffold_kind='result' AND json_extract(body,'$.result_id')=? AND json_extract(body,'$.revision')=?`, scope.AgentID, scope.ConversationID, id, revision).Scan(&event)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnavailable
		}
		if err != nil {
			return err
		}
		value, err = s.cfg.Journal.Read(ctx, tx, scope, event)
		return err
	})
	return value, err
}
