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
CREATE INDEX retract_event_exclusion ON retract_block(event_id);
CREATE INDEX retract_conversation_exclusion ON retract_block(person,conversation_id);
CREATE TRIGGER memory_no_forgotten_insert BEFORE INSERT ON memory_item
WHEN NEW.state='active' AND (
 EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=NEW.id AND r.state<>'active') OR
 EXISTS(SELECT 1 FROM json_each(NEW.record,'$.evidence') e JOIN retract_block b ON b.person=NEW.person
 WHERE json_extract(e.value,'$.kind')='event' AND
 (b.event_id=json_extract(e.value,'$.id') OR b.conversation_id=(SELECT conversation_id FROM conv_event WHERE id=json_extract(e.value,'$.id')))) OR
 EXISTS(SELECT 1 FROM json_each(NEW.record,'$.evidence') e JOIN retract_target r
 ON r.kind='memory' AND r.id=json_extract(e.value,'$.id')
 WHERE json_extract(e.value,'$.kind')='item' AND r.state<>'active')
)
BEGIN SELECT RAISE(ABORT,'forgotten_evidence'); END;
CREATE TRIGGER memory_no_forgotten_update BEFORE UPDATE ON memory_item
WHEN NEW.state='active'
AND NOT(OLD.state='trashed' AND EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=NEW.id AND r.state='active'))
AND (
 EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=NEW.id AND r.state<>'active') OR
 EXISTS(SELECT 1 FROM json_each(NEW.record,'$.evidence') e JOIN retract_block b ON b.person=NEW.person
 WHERE json_extract(e.value,'$.kind')='event' AND
 (b.event_id=json_extract(e.value,'$.id') OR b.conversation_id=(SELECT conversation_id FROM conv_event WHERE id=json_extract(e.value,'$.id')))) OR
 EXISTS(SELECT 1 FROM json_each(NEW.record,'$.evidence') e JOIN retract_target r
 ON r.kind='memory' AND r.id=json_extract(e.value,'$.id')
 WHERE json_extract(e.value,'$.kind')='item' AND r.state<>'active')
)
BEGIN SELECT RAISE(ABORT,'forgotten_evidence'); END;
`
