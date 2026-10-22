// SPDX-License-Identifier: Apache-2.0
package canon

// RecordsSchema stores publication intents and a disposable identity index.
// Markdown files and their Git history remain canonical.
const RecordsSchema = `
CREATE TABLE records_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), head TEXT NOT NULL,
 lock_token TEXT NOT NULL DEFAULT '', pending BLOB
);
INSERT INTO records_state(singleton,head) VALUES(1,'');
CREATE TABLE records_index (
 id TEXT PRIMARY KEY NOT NULL, path TEXT NOT NULL UNIQUE,
 blob TEXT NOT NULL, revision TEXT NOT NULL
);
`
