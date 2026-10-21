-- SPDX-License-Identifier: Apache-2.0
CREATE TABLE turn_outcome (
 turn_id TEXT PRIMARY KEY NOT NULL REFERENCES turn_runs(id),
 outcome BLOB NOT NULL,
 corrections INTEGER NOT NULL DEFAULT 0 CHECK(corrections>=0),
 repeats INTEGER NOT NULL DEFAULT 0 CHECK(repeats>=0),
 rework INTEGER NOT NULL DEFAULT 0 CHECK(rework>=0),
 not_asked INTEGER NOT NULL DEFAULT 0 CHECK(not_asked>=0)
);