// SPDX-License-Identifier: Apache-2.0
package canon

const presetEffectsSchema = `
CREATE TABLE authority_delegations (
 task_id TEXT PRIMARY KEY, person TEXT NOT NULL,
 source TEXT NOT NULL, source_task TEXT NOT NULL,
 receiver TEXT NOT NULL, limits BLOB NOT NULL
) WITHOUT ROWID;
CREATE TABLE coding_managers (
 id TEXT PRIMARY KEY, person TEXT NOT NULL, owner TEXT NOT NULL,
 unit TEXT NOT NULL, conversation TEXT NOT NULL, UNIQUE(person,unit)
) WITHOUT ROWID;
CREATE TABLE coding_assignments (
 id TEXT PRIMARY KEY, manager TEXT NOT NULL REFERENCES coding_managers(id),
 record BLOB NOT NULL
) WITHOUT ROWID;
`
