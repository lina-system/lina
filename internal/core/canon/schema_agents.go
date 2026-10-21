// SPDX-License-Identifier: Apache-2.0
package canon

const agentsSchema = `
CREATE TABLE agent_presets (id TEXT PRIMARY KEY, record BLOB NOT NULL);
CREATE TABLE agents (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, preset TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0), state BLOB NOT NULL,
 trashed_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE agent_revisions (
 agent_id TEXT NOT NULL REFERENCES agents(id), revision INTEGER NOT NULL,
 commit_id TEXT NOT NULL, person_id TEXT NOT NULL,
 PRIMARY KEY(agent_id,revision)
);
CREATE TABLE agent_preparations (
 turn_id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, revision INTEGER NOT NULL,
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions(agent_id,revision)
);
CREATE TABLE agent_bindings (
 channel TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), person_id TEXT NOT NULL
);
CREATE TABLE agent_actions (
 sequence INTEGER PRIMARY KEY, agent_id TEXT NOT NULL, person_id TEXT NOT NULL,
 action TEXT NOT NULL, at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE agent_first_run (singleton INTEGER PRIMARY KEY CHECK(singleton=1), agent_id TEXT NOT NULL);
CREATE TABLE agent_tombstones (agent_id TEXT PRIMARY KEY, deleted_at INTEGER NOT NULL);
`
