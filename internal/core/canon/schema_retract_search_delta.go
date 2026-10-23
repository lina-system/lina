// SPDX-License-Identifier: Apache-2.0
package canon

// Follow one candidate's item evidence towards its ancestors. Unlike the global
// exclusion view, this uses primary-key lookups and does not expand unrelated
// items. UNION terminates cycles and deduplicates shared ancestors. A missing
// ancestor is excluded only if it has a removal target, as in the existing view.
const retractMemoryCandidateVisible = `candidate.state='active' AND NOT EXISTS(
 WITH RECURSIVE ancestors(id) AS (
  SELECT candidate.id
  UNION
  SELECT json_extract(e.value,'$.id')
  FROM ancestors a JOIN memory_item m ON m.id=a.id, json_each(m.record,'$.evidence') e
  WHERE json_extract(e.value,'$.kind')='item'
 )
 SELECT 1 FROM ancestors a
 WHERE EXISTS(SELECT 1 FROM memory_item m WHERE m.id=a.id AND m.state<>'active')
 OR EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=a.id AND r.state<>'active')
)`

// The reverse walk still reads JSON evidence until the shared citation index
// lands. Its result is limited to this root and its dependents; neither removal
// nor recovery submits unrelated documents to the index or its fence triggers.
const retractMemoryAffected = `WITH RECURSIVE affected(id) AS (
 SELECT NEW.id
 UNION
 SELECT m.id FROM affected a JOIN memory_item m, json_each(m.record,'$.evidence') e
 ON json_extract(e.value,'$.id')=a.id WHERE json_extract(e.value,'$.kind')='item'
) SELECT id FROM affected`

const retractMemoryDeleteAffected = `
 DELETE FROM memory_search_text WHERE NEW.state<>'active'
 AND item_id IN (` + retractMemoryAffected + `);
`

const retractMemoryInsertAffected = `
 INSERT INTO memory_search_text(sequence,item_id,body)
 SELECT candidate.sequence,candidate.id,candidate.normalized FROM memory_item candidate
 WHERE NEW.state='active' AND candidate.id IN (` + retractMemoryAffected + `)
 AND NOT EXISTS(SELECT 1 FROM memory_search_text s WHERE s.item_id=candidate.id)
 AND ` + retractMemoryCandidateVisible

// Replace published triggers in a new migration; no canonical rows or existing
// indexes need rewriting. The memory index owner still handles the changed
// item's own state update, so that path refreshes only its dependents here.
const retractSearchDeltaSchema = `
DROP TRIGGER retract_fence_memory_text;
DROP TRIGGER retract_fence_memory_bigram;
DROP TRIGGER retract_refresh_memory_insert;
DROP TRIGGER retract_refresh_memory_update;
DROP TRIGGER retract_refresh_memory_state;

CREATE TRIGGER retract_fence_memory_text BEFORE INSERT ON memory_search_text
WHEN NOT EXISTS(SELECT 1 FROM memory_item candidate WHERE candidate.id=NEW.item_id
 AND ` + retractMemoryCandidateVisible + `)
BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER retract_fence_memory_bigram BEFORE INSERT ON memory_search_bigram
WHEN NOT EXISTS(SELECT 1 FROM memory_item candidate WHERE candidate.id=NEW.item_id
 AND ` + retractMemoryCandidateVisible + `)
BEGIN SELECT RAISE(IGNORE); END;

CREATE TRIGGER retract_refresh_memory_insert AFTER INSERT ON retract_target
WHEN NEW.kind='memory' BEGIN
` + retractMemoryDeleteAffected + retractMemoryInsertAffected + `;
END;
CREATE TRIGGER retract_refresh_memory_update AFTER UPDATE OF state ON retract_target
WHEN NEW.kind='memory' AND OLD.state<>NEW.state BEGIN
` + retractMemoryDeleteAffected + retractMemoryInsertAffected + `;
END;
CREATE TRIGGER retract_refresh_memory_state AFTER UPDATE OF state ON memory_item
WHEN OLD.state<>NEW.state BEGIN
` + retractMemoryDeleteAffected + retractMemoryInsertAffected + ` AND candidate.id<>NEW.id;
END;
`
