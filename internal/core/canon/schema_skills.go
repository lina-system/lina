// SPDX-License-Identifier: Apache-2.0
package canon

// SkillsSchema is installed by the shared published canon migration catalog.
const SkillsSchema = `
CREATE TABLE skill_versions (
 id TEXT NOT NULL, revision INTEGER NOT NULL, commit_id TEXT NOT NULL,
 name TEXT NOT NULL, description TEXT NOT NULL, content_hash TEXT NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 PRIMARY KEY(id,revision)
);
CREATE TABLE skill_heads (id TEXT PRIMARY KEY NOT NULL, revision INTEGER NOT NULL);
CREATE TABLE skill_undone (id TEXT NOT NULL, content_hash TEXT NOT NULL, PRIMARY KEY(id,content_hash));
CREATE TABLE skill_repo (singleton INTEGER PRIMARY KEY CHECK(singleton=1), commit_id TEXT NOT NULL);
`
