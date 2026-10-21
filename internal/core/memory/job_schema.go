// SPDX-License-Identifier: Apache-2.0
package memory

// JobsSchema is installed by the composing schema owner, alongside jobs.Schema
// and the shared budget schema. Starting a worker never creates tables.
const JobsSchema = `
CREATE TABLE memory_maintenance_choice (
 person TEXT NOT NULL, holder TEXT NOT NULL, revision INTEGER NOT NULL,
 record BLOB NOT NULL CHECK(json_valid(record)), PRIMARY KEY(person,holder)
);
CREATE TABLE memory_maintenance_proposal (
 id TEXT PRIMARY KEY, person TEXT NOT NULL, holder TEXT NOT NULL,
 expires INTEGER NOT NULL, record BLOB NOT NULL CHECK(json_valid(record))
);
CREATE TABLE memory_person_brief (
 person TEXT NOT NULL, agent TEXT NOT NULL, generation INTEGER NOT NULL,
 record BLOB NOT NULL CHECK(json_valid(record)), PRIMARY KEY(person,agent)
);
`
