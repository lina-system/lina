// SPDX-License-Identifier: Apache-2.0
package canon

// Evidence consumers opt into these projections. The original ledger remains
// readable for authorized conversation display; forgetting memory does not
// delete the person's conversation. Canonical derived items retain provenance
// for maintenance, but cannot enter recall while an ancestor is suppressed.
const retractViewsSchema = `
CREATE VIEW retract_visible_event AS
SELECT e.* FROM conv_event e
WHERE NOT EXISTS(SELECT 1 FROM retract_excluded_event x WHERE x.event_id=e.id)
AND NOT EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='event' AND r.id=e.id AND r.state<>'active');
CREATE VIEW retract_memory_exclusion AS
WITH RECURSIVE excluded(id) AS (
 SELECT id FROM retract_target WHERE kind='memory' AND state<>'active'
 UNION SELECT id FROM memory_item WHERE state<>'active'
 UNION
 SELECT m.id FROM memory_item m,json_each(m.record,'$.evidence') e
 JOIN excluded x ON x.id=json_extract(e.value,'$.id') WHERE json_extract(e.value,'$.kind')='item'
)
SELECT id FROM excluded;
CREATE VIEW retract_visible_memory AS
SELECT m.* FROM memory_item m WHERE NOT EXISTS(SELECT 1 FROM retract_memory_exclusion x WHERE x.id=m.id);
`
