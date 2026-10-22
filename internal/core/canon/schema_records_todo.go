// SPDX-License-Identifier: Apache-2.0
package canon

// recordsTodoSchema is disposable. Mark the index stale so existing published
// files are backfilled by the same rebuild used on startup and recovery.
const recordsTodoSchema = `
CREATE TABLE records_todo (
 id TEXT PRIMARY KEY NOT NULL REFERENCES records_index(id) ON DELETE CASCADE,
 fields TEXT NOT NULL,
 when_value TEXT NOT NULL, deadline TEXT NOT NULL, repeat_next TEXT NOT NULL
);
UPDATE records_index_state SET head='unbuilt';
`
