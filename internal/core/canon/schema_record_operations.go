// SPDX-License-Identifier: Apache-2.0
package canon

// Undo and admission receipts are machine state. Records remain Git-backed.
const recordOperationsSchema = `
CREATE TABLE record_operations (
 handle TEXT PRIMARY KEY NOT NULL,
 person TEXT NOT NULL, agent TEXT NOT NULL, record_id TEXT NOT NULL,
 before_blob TEXT NOT NULL, revision TEXT NOT NULL,
 undo_revision TEXT NOT NULL DEFAULT '', notice TEXT NOT NULL,
 candidate INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE record_candidates (
 number INTEGER PRIMARY KEY AUTOINCREMENT,
 person TEXT NOT NULL, source TEXT NOT NULL, source_revision TEXT NOT NULL,
 class TEXT NOT NULL, equivalence TEXT NOT NULL, proposal BLOB NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending', record_id TEXT NOT NULL DEFAULT '',
 undo_handle TEXT NOT NULL DEFAULT '',
 UNIQUE(person,source,equivalence)
);
`
