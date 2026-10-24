// SPDX-License-Identifier: Apache-2.0
package canon

// MiniAppsSchema holds the registry, the numbered file revisions and the two
// append-only logs. Revisions keep only file identities; bytes live on disk.
const MiniAppsSchema = `
CREATE TABLE mini_app (
 id TEXT PRIMARY KEY,
 person TEXT NOT NULL,
 agent TEXT NOT NULL,
 name TEXT NOT NULL,
 request TEXT NOT NULL,
 manifest BLOB NOT NULL,
 revision INTEGER NOT NULL DEFAULT 0 CHECK(revision >= 0),
 allowed_revision INTEGER NOT NULL DEFAULT 0 CHECK(allowed_revision >= 0),
 token_hash TEXT NOT NULL DEFAULT '',
 cookie_name TEXT NOT NULL,
 store_shape BLOB NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 removed INTEGER NOT NULL DEFAULT 0 CHECK(removed IN (0,1)),
 create_key TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX mini_app_create_key ON mini_app(person, create_key) WHERE create_key <> '';
CREATE INDEX mini_app_person ON mini_app(person, removed);
CREATE TABLE mini_app_revision (
 app TEXT NOT NULL REFERENCES mini_app(id),
 revision INTEGER NOT NULL CHECK(revision >= 1),
 request TEXT NOT NULL,
 summary TEXT NOT NULL DEFAULT '',
 files BLOB NOT NULL,
 store_snapshot INTEGER NOT NULL DEFAULT 0 CHECK(store_snapshot IN (0,1)),
 created_at INTEGER NOT NULL,
 PRIMARY KEY(app, revision)
);
CREATE TABLE mini_app_change (
 id TEXT PRIMARY KEY,
 app TEXT NOT NULL REFERENCES mini_app(id),
 revision INTEGER NOT NULL,
 request TEXT NOT NULL,
 summary TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX mini_app_change_app ON mini_app_change(app, created_at);
CREATE TABLE mini_app_event (
 id TEXT PRIMARY KEY,
 app TEXT NOT NULL REFERENCES mini_app(id),
 kind TEXT NOT NULL,
 detail TEXT NOT NULL,
 created_at INTEGER NOT NULL
);
CREATE INDEX mini_app_event_app ON mini_app_event(app, created_at);
`
