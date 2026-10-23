// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/kit/result"
)

// RecordDoneCompletion projects a Core-authored completion in its producer's
// transaction. A replay cannot count another breach or change an old decision.
func RecordDoneCompletion(ctx context.Context, tx *Tx, c *result.Completion) error {
	if c == nil {
		return nil
	}
	if c.ID == "" || c.AgentID == "" || (c.State != "done" && c.State != "claimed") {
		return errors.New("invalid completion")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT record FROM done_completions WHERE id=?", c.ID).Scan(&old)
	if err == nil {
		if !bytes.Equal(raw, old) {
			return errors.New("completion conflict")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO done_completions(id,agent_id,state,record) VALUES(?,?,?,?)", c.ID, c.AgentID, c.State, raw)
	return err
}
