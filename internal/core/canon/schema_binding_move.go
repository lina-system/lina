// SPDX-License-Identifier: Apache-2.0
package canon

// BindingMoveSchema keeps what a bound turn did, so a later move can undo or
// repeat it, and the moves themselves with their counter and replay case.
const BindingMoveSchema = `
CREATE TABLE binding_effects (
 input_event TEXT NOT NULL REFERENCES conv_event(id),
 turn_id TEXT NOT NULL,
 call_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('create','edit','steer','start')),
 target TEXT NOT NULL,
 record_id TEXT NOT NULL DEFAULT '',
 before_body BLOB,
 before_revision TEXT NOT NULL DEFAULT '',
 after_revision TEXT NOT NULL DEFAULT '',
 payload BLOB NOT NULL,
 left_person INTEGER NOT NULL DEFAULT 0 CHECK(left_person IN (0,1)),
 PRIMARY KEY(input_event,turn_id,call_id)
);
CREATE TABLE binding_moves (
 id TEXT PRIMARY KEY NOT NULL,
 input_event TEXT NOT NULL REFERENCES conv_event(id),
 person TEXT NOT NULL,
 agent TEXT NOT NULL,
 conversation TEXT NOT NULL,
 decided_by TEXT NOT NULL,
 from_target TEXT NOT NULL,
 to_target TEXT NOT NULL,
 record BLOB NOT NULL,
 moved_at TEXT NOT NULL
);
CREATE INDEX binding_moves_agent ON binding_moves(agent,moved_at);
`
