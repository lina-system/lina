// SPDX-License-Identifier: Apache-2.0
package canon

const ResultsSchema = `
CREATE TABLE result_heads (
 id TEXT PRIMARY KEY NOT NULL, person TEXT NOT NULL, agent TEXT NOT NULL,
 conversation TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0),
 event_id TEXT NOT NULL, save_key TEXT
);
CREATE TABLE result_saves (
 key TEXT PRIMARY KEY NOT NULL, result_id TEXT NOT NULL UNIQUE REFERENCES result_heads(id),
 state TEXT NOT NULL CHECK(state IN ('unknown','applied')), reference BLOB NOT NULL
);
`

const PlanningSchema = `
CREATE TABLE plan_drafts (
 result_id TEXT PRIMARY KEY REFERENCES result_heads(id),
 root_id TEXT NOT NULL, root_revision TEXT NOT NULL
);
CREATE TABLE plan_confirmations (
 result_id TEXT PRIMARY KEY REFERENCES result_heads(id), revision INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('unknown','applied')),
 changes BLOB NOT NULL, receipt BLOB NOT NULL
);
`
