// SPDX-License-Identifier: Apache-2.0
package canon

// The contract step runs only after the reader switch and its activation backup.
// Published migrations above it still populate citations for older generations.
const storeContractSchema = `
DROP TRIGGER ledger_search_enqueue;
DROP TRIGGER memory_item_search_insert;
DROP TRIGGER memory_item_search_update;
DROP TRIGGER memory_item_search_delete;
DROP TRIGGER records_text_insert;
DROP TRIGGER records_text_delete;
DROP TRIGGER records_korean_insert;
DROP TRIGGER records_korean_delete;
DROP TRIGGER retract_purge_ledger;
DROP TRIGGER retract_recover_ledger;
DROP TRIGGER retract_refresh_memory_insert;
DROP TRIGGER retract_refresh_memory_update;
DROP TRIGGER retract_refresh_memory_state;
DROP TABLE ledger_search;
DROP TABLE ledger_search_text;
DROP TABLE ledger_search_bigram;
DROP TABLE ledger_search_pending;
DROP TABLE memory_search;
DROP TABLE memory_search_text;
DROP TABLE memory_search_bigram;
DROP TABLE records_fts;
DROP TABLE records_korean_fts;
DROP TABLE records_korean_bigram;
DELETE FROM search_index_config WHERE name IN ('ledger','memory','records_korean');

ALTER TABLE cite ADD COLUMN source_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE cite ADD COLUMN occurred_at INTEGER NOT NULL DEFAULT 0;
-- Preserve source metadata from the revision which introduced each interval.
UPDATE cite SET (source_agent,occurred_at)=(
 SELECT coalesce(json_extract(e.value,'$.source_agent'),''),coalesce(json_extract(e.value,'$.occurred_at'),0)
 FROM memory_revision r,json_each(r.record,'$.evidence') e
 WHERE r.item_id=cite.view_id AND r.revision=CAST(cite.added_revision AS INTEGER) AND e.key=cite.ordinal
) WHERE view_kind='memory' AND EXISTS(
 SELECT 1 FROM memory_revision r,json_each(r.record,'$.evidence') e
 WHERE r.item_id=cite.view_id AND r.revision=CAST(cite.added_revision AS INTEGER) AND e.key=cite.ordinal);

UPDATE cite SET (source_agent,occurred_at)=(
 SELECT coalesce(json_extract(e.value,'$.source_agent'),''),coalesce(json_extract(e.value,'$.occurred_at'),0)
 FROM memory_item m,json_each(m.record,'$.evidence') e
 WHERE m.id=cite.view_id AND e.key=cite.ordinal)
WHERE view_kind='memory' AND dropped_revision IS NULL AND source_agent='' AND EXISTS(
 SELECT 1 FROM memory_item m,json_each(m.record,'$.evidence') e WHERE m.id=cite.view_id AND e.key=cite.ordinal);
WITH pointers AS (
 SELECT json_array(b.person,b.agent) AS id,b.generation,
 row_number() OVER(PARTITION BY b.person,b.agent ORDER BY s.key,e.key)-1 AS ordinal,e.value
 FROM memory_person_brief b,json_each(b.record,'$.sentences') s,json_each(s.value,'$.citations') e
)
UPDATE cite SET (source_agent,occurred_at)=(
 SELECT coalesce(json_extract(p.value,'$.source_agent'),''),coalesce(json_extract(p.value,'$.occurred_at'),0)
 FROM pointers p WHERE p.id=cite.view_id AND p.ordinal=cite.ordinal)
WHERE view_kind='person_brief' AND dropped_revision IS NULL AND EXISTS(SELECT 1 FROM pointers p WHERE p.id=cite.view_id AND p.ordinal=cite.ordinal);

DROP VIEW retract_visible_memory;
DROP VIEW retract_memory_exclusion;
CREATE VIEW retract_memory_exclusion AS
WITH RECURSIVE excluded(id) AS (
 SELECT id FROM retract_target WHERE kind='memory' AND state<>'active'
 UNION SELECT id FROM memory_item WHERE state<>'active'
 UNION SELECT c.view_id FROM excluded x JOIN cite c ON c.target_kind='memory' AND c.target_id=x.id
 WHERE c.view_kind='memory' AND c.dropped_revision IS NULL
) SELECT id FROM excluded;
CREATE VIEW retract_visible_memory AS
SELECT m.* FROM memory_item m WHERE NOT EXISTS(SELECT 1 FROM retract_memory_exclusion x WHERE x.id=m.id);

UPDATE memory_item SET record=json_remove(record,'$.evidence');
UPDATE memory_revision SET record=json_remove(record,'$.evidence');
DROP TRIGGER memory_no_forgotten_insert;
DROP TRIGGER memory_no_forgotten_update;
CREATE TRIGGER memory_no_forgotten_insert BEFORE INSERT ON memory_item
WHEN NEW.state='active' AND EXISTS(SELECT 1 FROM retract_target WHERE kind='memory' AND id=NEW.id AND state<>'active')
BEGIN SELECT RAISE(ABORT,'forgotten_evidence'); END;
CREATE TRIGGER memory_no_forgotten_update BEFORE UPDATE ON memory_item
WHEN NEW.state='active'
AND NOT(OLD.state='trashed' AND EXISTS(SELECT 1 FROM retract_target WHERE kind='memory' AND id=NEW.id AND state='active'))
AND (EXISTS(SELECT 1 FROM retract_target WHERE kind='memory' AND id=NEW.id AND state<>'active')
 OR EXISTS(SELECT 1 FROM cite c WHERE c.view_kind='memory' AND c.view_id=NEW.id AND c.dropped_revision IS NULL AND (
  (c.target_kind='memory' AND c.target_id IN (SELECT id FROM retract_memory_exclusion))
  OR (c.target_kind='event' AND EXISTS(SELECT 1 FROM retract_block b WHERE b.person=NEW.person AND
   (b.event_id=c.target_id OR b.conversation_id=(SELECT conversation_id FROM conv_event WHERE id=c.target_id))))
  OR (c.target_kind='event' AND EXISTS(SELECT 1 FROM retract_excluded_event x JOIN retract_target r ON r.kind='memory' AND r.id=x.item_id WHERE x.event_id=c.target_id AND r.person=NEW.person))
 )))
BEGIN SELECT RAISE(ABORT,'forgotten_evidence'); END;
CREATE TRIGGER cite_no_forgotten_memory BEFORE INSERT ON cite
WHEN NEW.view_kind='memory' AND NEW.dropped_revision IS NULL
AND EXISTS(SELECT 1 FROM memory_item m WHERE m.id=NEW.view_id AND m.state='active' AND (
 (NEW.target_kind='memory' AND NEW.target_id IN (SELECT id FROM retract_memory_exclusion))
 OR (NEW.target_kind='event' AND EXISTS(SELECT 1 FROM retract_block b WHERE b.person=m.person AND
  (b.event_id=NEW.target_id OR b.conversation_id=(SELECT conversation_id FROM conv_event WHERE id=NEW.target_id))))
 OR (NEW.target_kind='event' AND EXISTS(SELECT 1 FROM retract_excluded_event x JOIN retract_target r ON r.kind='memory' AND r.id=x.item_id WHERE x.event_id=NEW.target_id AND r.person=m.person))
))
BEGIN SELECT RAISE(ABORT,'forgotten_evidence'); END;


-- Sentences retain counts, not provenance. Their ordered pointers live in cite.
UPDATE memory_person_brief SET record=json_set(record,'$.sentences',json(coalesce((
 SELECT json_group_array(json(json_remove(json_set(s.value,'$.citation_count',json_array_length(s.value,'$.citations')),'$.citations')))
 FROM json_each(memory_person_brief.record,'$.sentences') s
),'[]')));

DROP TABLE sum_cover;
DROP TABLE sum_edge;
CREATE VIEW sum_edge AS
 SELECT view_id AS parent_id,target_id AS child_id,ordinal FROM cite
 WHERE view_kind='summary' AND target_kind='summary' AND dropped_revision IS NULL;
CREATE VIEW sum_cover AS
 SELECT view_id AS node_id,target_id AS event_id,
 ordinal-(SELECT count(*) FROM sum_edge e WHERE e.parent_id=c.view_id) AS ordinal,
 CASE role WHEN 'covers' THEN '' ELSE role END AS reason FROM cite c
 WHERE view_kind='summary' AND target_kind='event' AND dropped_revision IS NULL;
CREATE UNIQUE INDEX cite_summary_event_owner ON cite(target_id)
 WHERE view_kind='summary' AND target_kind='event' AND dropped_revision IS NULL;
CREATE UNIQUE INDEX cite_summary_child_owner ON cite(target_id)
 WHERE view_kind='summary' AND target_kind='summary' AND dropped_revision IS NULL;
CREATE TRIGGER cite_summary_shape BEFORE INSERT ON cite
WHEN NEW.view_kind='summary' AND
 (NEW.target_kind NOT IN ('event','summary')
 OR (NEW.target_kind='event' AND NEW.role='cites')
 OR (NEW.target_kind='summary' AND NEW.role<>'cites'))
BEGIN SELECT RAISE(ABORT,'invalid_summary_citation'); END;
CREATE TRIGGER cite_summary_node_delete BEFORE DELETE ON sum_node
WHEN EXISTS(SELECT 1 FROM cite WHERE view_kind='summary' AND dropped_revision IS NULL
 AND (view_id=OLD.id OR (target_kind='summary' AND target_id=OLD.id)))
BEGIN SELECT RAISE(ABORT,'summary_in_use'); END;
CREATE TRIGGER cite_summary_child BEFORE INSERT ON cite
WHEN NEW.view_kind='summary' AND NEW.target_kind='summary' AND NEW.dropped_revision IS NULL
AND NOT EXISTS(SELECT 1 FROM sum_node p JOIN sum_node c ON p.conversation_id=c.conversation_id
 WHERE p.id=NEW.view_id AND c.id=NEW.target_id AND p.level>c.level AND c.status='ready' AND p.epoch=c.epoch)
BEGIN SELECT RAISE(ABORT,'invalid_summary_child'); END;
CREATE TRIGGER cite_summary_event BEFORE INSERT ON cite
WHEN NEW.view_kind='summary' AND NEW.target_kind='event' AND NEW.dropped_revision IS NULL
AND NOT EXISTS(SELECT 1 FROM sum_node n JOIN conv_event e ON e.conversation_id=n.conversation_id
 WHERE n.id=NEW.view_id AND e.id=NEW.target_id)
BEGIN SELECT RAISE(ABORT,'invalid_summary_event'); END;
`
