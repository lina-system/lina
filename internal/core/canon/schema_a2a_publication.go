// SPDX-License-Identifier: Apache-2.0
package canon

const a2aSchema = `
CREATE TABLE a2a_tasks (
 id TEXT PRIMARY KEY NOT NULL,
 publication TEXT NOT NULL,
 caller TEXT NOT NULL,
 phase TEXT NOT NULL,
 updated TEXT NOT NULL,
 record BLOB NOT NULL
);
CREATE INDEX a2a_caller_tasks ON a2a_tasks(publication,caller,updated DESC,id DESC);
CREATE TABLE a2a_messages (
 publication TEXT NOT NULL,
 caller TEXT NOT NULL,
 message TEXT NOT NULL,
 hash TEXT NOT NULL,
 task TEXT NOT NULL REFERENCES a2a_tasks(id),
 PRIMARY KEY(publication,caller,message)
);
`
