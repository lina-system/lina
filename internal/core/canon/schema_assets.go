// SPDX-License-Identifier: Apache-2.0
package canon

const schemaAssets = `
CREATE TABLE assets (
 id TEXT PRIMARY KEY,
 area TEXT NOT NULL CHECK(area IN ('user','lina')),
 observed_path TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0),
 person_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 trashed_at INTEGER NOT NULL DEFAULT 0 CHECK(trashed_at >= 0)
);
CREATE TABLE asset_revisions (
 asset_id TEXT NOT NULL REFERENCES assets(id),
 revision INTEGER NOT NULL CHECK(revision > 0),
 path TEXT NOT NULL UNIQUE,
 hash TEXT NOT NULL CHECK(length(hash) = 64),
 size INTEGER NOT NULL CHECK(size >= 0),
 person_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 PRIMARY KEY(asset_id, revision)
);
CREATE TABLE asset_exports (
 id TEXT PRIMARY KEY,
 asset_id TEXT NOT NULL,
 revision INTEGER NOT NULL,
 hash TEXT NOT NULL,
 destination TEXT NOT NULL,
 person_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('unknown','delivered')),
 FOREIGN KEY(asset_id, revision) REFERENCES asset_revisions(asset_id, revision)
);
CREATE TABLE asset_trash_events (
 sequence INTEGER PRIMARY KEY,
 asset_id TEXT NOT NULL REFERENCES assets(id),
 action TEXT NOT NULL CHECK(action IN ('trash','restore')),
 at INTEGER NOT NULL,
 person_id TEXT NOT NULL,
 agent_id TEXT NOT NULL
);
`
