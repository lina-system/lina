// SPDX-License-Identifier: Apache-2.0
package canon

import "context"

// sqlite-vec forbids virtual-table writes from SQL triggers. Triggers queue
// identities instead; the sole canon writer removes them before the SAME
// transaction commits. A failure rolls back source removal and vector removal.
func flushVectorDeletes(ctx context.Context, tx *Tx) error {
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM recall_vec_delete)").Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM recall_vec WHERE rowid IN (SELECT doc FROM recall_vec_delete); DELETE FROM recall_vec_delete`)
	return err
}
