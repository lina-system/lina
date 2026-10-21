// SPDX-License-Identifier: Apache-2.0
package canon

const retractSchema = `
CREATE TABLE retract_meta (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 epoch INTEGER NOT NULL CHECK(epoch>=0)
);
INSERT INTO retract_meta VALUES(1,0);
CREATE TABLE removal_applied (
 sequence INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, digest TEXT NOT NULL
);
CREATE TABLE retract_target (
 kind TEXT NOT NULL CHECK(kind IN ('memory','event','source')), id TEXT NOT NULL,
 person TEXT NOT NULL, holder TEXT NOT NULL, agent TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('trashed','active','erased','disconnected')),
 at INTEGER NOT NULL, provenance BLOB NOT NULL,
 operation_id TEXT NOT NULL REFERENCES removal_applied(id),
 PRIMARY KEY(kind,id)
);
CREATE TRIGGER retract_target_requires_removal BEFORE INSERT ON retract_target
WHEN NOT EXISTS(SELECT 1 FROM removal_applied WHERE id=NEW.operation_id)
BEGIN SELECT RAISE(ABORT,'removal_required'); END;
CREATE TRIGGER retract_target_update_requires_removal BEFORE UPDATE ON retract_target
WHEN NOT EXISTS(SELECT 1 FROM removal_applied WHERE id=NEW.operation_id)
BEGIN SELECT RAISE(ABORT,'removal_required'); END;
CREATE TABLE retract_block (
 item_id TEXT NOT NULL, person TEXT NOT NULL, holder TEXT NOT NULL,
 event_id TEXT NOT NULL DEFAULT '', conversation_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(item_id,event_id,conversation_id)
);
`
