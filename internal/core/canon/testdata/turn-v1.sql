-- SPDX-License-Identifier: Apache-2.0

CREATE TABLE turn_runs (
 id TEXT PRIMARY KEY NOT NULL,
 conversation TEXT NOT NULL,
 inbox_id INTEGER NOT NULL UNIQUE REFERENCES inbox(id),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 state TEXT NOT NULL CHECK(state IN ('active','completed','aborted','failed'))
);
CREATE TABLE turn_items (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, item BLOB NOT NULL,
 PRIMARY KEY(turn_id,round,ordinal)
);
CREATE TABLE turn_calls (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, call_id TEXT NOT NULL, call BLOB NOT NULL, result BLOB,
 PRIMARY KEY(turn_id,round,ordinal), UNIQUE(turn_id,call_id)
);
CREATE TABLE turn_partials (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, body TEXT NOT NULL,
 PRIMARY KEY(turn_id,round,ordinal)
);
