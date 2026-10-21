// SPDX-License-Identifier: Apache-2.0
package canon

import "github.com/ncruces/go-sqlite3"

// The immutable ledger remains in main. Every canon reader gets the same
// content projection, including ledger search and recall_person's fallback.
// Writer transactions retain access to original provenance for deletion,
// recovery and evidence checks. Views are connection-local, never canon.
func installRetractionViews(conn *sqlite3.Conn) error {
	stmt, _, err := conn.Prepare("SELECT count(*) FROM main.sqlite_schema WHERE type='table' AND name IN ('retract_block','retract_target','conv_event')")
	if err != nil {
		return err
	}
	ready := stmt.Step() && stmt.ColumnInt(0) == 3
	if err := stmt.Close(); err != nil {
		return err
	}
	if !ready {
		return nil
	}
	return conn.Exec(`CREATE TEMP VIEW conv_event AS
SELECT e.* FROM main.conv_event e
WHERE NOT EXISTS(SELECT 1 FROM main.retract_block b WHERE b.event_id=e.id)
AND NOT EXISTS(SELECT 1 FROM main.retract_target r WHERE r.kind='event' AND r.id=e.id AND r.state<>'active')`)
}
