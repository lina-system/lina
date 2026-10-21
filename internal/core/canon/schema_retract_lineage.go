// SPDX-License-Identifier: Apache-2.0
package canon

// Lineage is derived from immutable replay/view links. Root exclusions remain
// in the removal record; this projection also covers copies admitted later and
// is rebuilt from those roots when an older generation is recovered.
const retractLineageSchema = `
CREATE TABLE retract_excluded_event (
 item_id TEXT NOT NULL, event_id TEXT NOT NULL,
 PRIMARY KEY(item_id,event_id)
);
CREATE INDEX retract_excluded_event_id ON retract_excluded_event(event_id);
CREATE VIEW retract_lineage AS
WITH RECURSIVE excluded(item_id,event_id) AS (
 SELECT item_id,event_id FROM retract_block WHERE event_id<>''
 UNION
 SELECT x.item_id,e.id FROM excluded x JOIN conv_event e ON e.replay_of=x.event_id
 UNION
 SELECT x.item_id,r.event_id FROM excluded x JOIN ev_rel r ON r.target_id=x.event_id AND r.kind='view_of'
)
SELECT item_id,event_id FROM excluded;
CREATE TRIGGER retract_expand_roots AFTER INSERT ON retract_block WHEN NEW.event_id<>'' BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage WHERE item_id=NEW.item_id;
END;
CREATE TRIGGER retract_remove_roots AFTER DELETE ON retract_block WHEN OLD.event_id<>'' BEGIN
 DELETE FROM retract_excluded_event WHERE item_id=OLD.item_id;
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage WHERE item_id=OLD.item_id;
END;
CREATE TRIGGER retract_expand_replay AFTER INSERT ON conv_event
WHEN NEW.replay_of IS NOT NULL AND EXISTS(SELECT 1 FROM retract_excluded_event WHERE event_id=NEW.replay_of)
BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage;
END;
CREATE TRIGGER retract_expand_view AFTER INSERT ON ev_rel
WHEN NEW.kind='view_of' AND EXISTS(SELECT 1 FROM retract_excluded_event WHERE event_id=NEW.target_id)
BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage;
END;
`
