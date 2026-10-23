// SPDX-License-Identifier: Apache-2.0
package canon

const A2ASchema = `
CREATE TABLE a2a_connections (
 id TEXT PRIMARY KEY, person TEXT NOT NULL, revision TEXT NOT NULL,
 enabled INTEGER NOT NULL, record BLOB NOT NULL
);
CREATE TABLE a2a_calls (
 id TEXT PRIMARY KEY, person TEXT NOT NULL, agent TEXT NOT NULL,
 conversation TEXT NOT NULL, connection TEXT NOT NULL, record BLOB NOT NULL
);
CREATE INDEX a2a_calls_origin ON a2a_calls(person,agent,conversation);
`
