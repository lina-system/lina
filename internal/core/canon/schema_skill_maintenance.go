// SPDX-License-Identifier: Apache-2.0
package canon

const skillMaintenanceSchema = `
CREATE TABLE skill_maintenance (
 id TEXT NOT NULL, revision INTEGER NOT NULL, operation_hash TEXT NOT NULL,
 kind TEXT NOT NULL, undone INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(id,revision)
);
CREATE TABLE skill_maintenance_members (
 operation_id TEXT NOT NULL, operation_revision INTEGER NOT NULL,
 id TEXT NOT NULL, revision INTEGER NOT NULL,
 PRIMARY KEY(id,revision)
);
CREATE TABLE skill_maintenance_notices (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, receipt BLOB NOT NULL
);
`
