// SPDX-License-Identifier: Apache-2.0
package canon

// retractCiteVisibleMemory is the source-view fence for the shared index. It
// walks a candidate's citation ancestors through the cite primary key instead
// of decoding the record JSON. A missing ancestor is excluded only when it has
// a removal target, as in the legacy projection.
const retractCiteVisibleMemory = `candidate.state='active' AND NOT EXISTS(
 WITH RECURSIVE ancestors(id) AS (
  SELECT candidate.id
  UNION
  SELECT c.target_id FROM ancestors a JOIN cite c
   ON c.view_kind='memory' AND c.view_id=a.id
  WHERE c.dropped_revision IS NULL AND c.target_kind='memory'
 )
 SELECT 1 FROM ancestors a
 WHERE EXISTS(SELECT 1 FROM memory_item m WHERE m.id=a.id AND m.state<>'active')
 OR EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=a.id AND r.state<>'active')
)`

// hiddenFromTarget lists the documents a removal hides: the target itself and
// every view that cites it, transitively, through cite_target.
const hiddenFromTarget = `WITH RECURSIVE affected(kind,id) AS (
  SELECT NEW.kind,NEW.id
  UNION
  SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
   ON c.target_kind=a.kind AND c.target_id=a.id
  WHERE c.dropped_revision IS NULL
  AND ((a.kind='event' AND c.view_kind='summary') OR (a.kind<>'event' AND c.view_kind IN ('summary','memory','record')))
 )
 SELECT kind,id FROM affected`

// One retraction path: removal, recovery and erase update the shared index in
// their own transaction, and the walk follows cite_target so its cost is the
// number of views the change hides. A memory removal reaches only the target
// and the items whose item evidence reaches it; an erased event hides the event
// and the summaries that cover it, while memory items resting on it stay until
// the person forgets them (conversation-and-memory.md, Correction, retraction
// and deletion).
const retractCiteSchema = `
DROP VIEW recall_source;
CREATE VIEW recall_source AS
 SELECT 'event' AS kind,e.id AS source_id,'0' AS revision,
 coalesce((SELECT min(o.person_id) FROM conv_event o WHERE o.conversation_id=e.conversation_id
 AND o.speaker_kind='person' HAVING count(DISTINCT o.person_id)=1),'') AS person,
 e.agent_id AS holder,e.conversation_id AS conversation,e.occurred_at,e.speaker_kind,e.origin,
 e.replay_of IS NOT NULL AS replay,e.body AS body
 FROM retract_visible_event e
 UNION ALL
 SELECT 'summary',n.id,cast(n.revision AS TEXT),
 coalesce((SELECT min(e.person_id) FROM conv_event e WHERE e.conversation_id=n.conversation_id
 AND e.speaker_kind='person' HAVING count(DISTINCT e.person_id)=1),''),
 coalesce((SELECT e.agent_id FROM conv_event e WHERE e.conversation_id=n.conversation_id ORDER BY e.rowid LIMIT 1),''),
 n.conversation_id,n.end_at,'system','{"kind":"native"}',0,n.text
 FROM sum_node n WHERE n.status='ready'
 UNION ALL
 SELECT 'memory',candidate.id,cast(coalesce(json_extract(candidate.record,'$.revision'),1) AS TEXT),candidate.person,candidate.holder,'',
 strftime('%Y-%m-%dT%H:%M:%SZ',candidate.valid_from,'unixepoch'),'',
 json_object('kind',CASE json_extract(candidate.record,'$.basis') WHEN 'imported' THEN 'import' ELSE 'native' END),0,candidate.content
 FROM memory_item candidate WHERE ` + retractCiteVisibleMemory + `
 UNION ALL
 SELECT 'record',r.id,i.revision,r.person,'all','',coalesce(a.last_activity,''),'','{"kind":"native"}',0,r.title||char(10)||r.note
 FROM records_text r JOIN records_index i ON i.id=r.id LEFT JOIN records_activity a ON a.id=r.id;

DROP TRIGGER recall_doc_delete;
CREATE TRIGGER recall_doc_delete AFTER DELETE ON recall_doc BEGIN
 DELETE FROM recall_text WHERE rowid=OLD.id;
 DELETE FROM recall_pair WHERE rowid=OLD.id;
 DELETE FROM recall_pending WHERE kind=OLD.kind AND source_id=OLD.source_id;
END;

-- The citation walk replaces the bridges that reacted to the old memory and
-- ledger indexes. Those indexes keep their own triggers until the contract step.
DROP TRIGGER recall_event_hidden;
DROP TRIGGER recall_event_target_insert;
DROP TRIGGER recall_event_target_update;
DROP TRIGGER recall_memory_hidden;

CREATE TRIGGER retract_recall_target_insert AFTER INSERT ON retract_target
WHEN NEW.state<>'active' BEGIN
 DELETE FROM recall_doc WHERE (kind,source_id) IN (` + hiddenFromTarget + `);
END;

CREATE TRIGGER retract_recall_target_hide AFTER UPDATE OF state ON retract_target
WHEN NEW.state<>'active' AND OLD.state<>NEW.state BEGIN
 DELETE FROM recall_doc WHERE (kind,source_id) IN (` + hiddenFromTarget + `);
END;

CREATE TRIGGER retract_recall_target_restore AFTER UPDATE OF state ON retract_target
WHEN NEW.state='active' AND OLD.state<>NEW.state BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id)
 WITH RECURSIVE affected(kind,id) AS (
  SELECT NEW.kind,NEW.id
  UNION
  SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
   ON c.target_kind=a.kind AND c.target_id=a.id
  WHERE c.dropped_revision IS NULL
  AND ((a.kind='event' AND c.view_kind='summary') OR (a.kind<>'event' AND c.view_kind IN ('summary','memory','record')))
 )
 SELECT a.kind,a.id FROM affected a
 WHERE EXISTS(SELECT 1 FROM recall_source s WHERE s.kind=a.kind AND s.source_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM recall_doc d WHERE d.kind=a.kind AND d.source_id=a.id);
END;

CREATE TRIGGER retract_recall_excluded_insert AFTER INSERT ON retract_excluded_event BEGIN
 DELETE FROM recall_doc WHERE (kind,source_id) IN (
  WITH RECURSIVE affected(kind,id) AS (
   SELECT 'event',NEW.event_id
   UNION
   SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
    ON c.target_kind=a.kind AND c.target_id=a.id
   WHERE c.dropped_revision IS NULL AND c.view_kind='summary'
  ) SELECT kind,id FROM affected);
END;

CREATE TRIGGER retract_recall_excluded_delete AFTER DELETE ON retract_excluded_event BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id)
 WITH RECURSIVE affected(kind,id) AS (
  SELECT 'event',OLD.event_id
  UNION
  SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
   ON c.target_kind=a.kind AND c.target_id=a.id
  WHERE c.dropped_revision IS NULL AND c.view_kind='summary'
 )
 SELECT a.kind,a.id FROM affected a
 WHERE EXISTS(SELECT 1 FROM recall_source s WHERE s.kind=a.kind AND s.source_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM recall_doc d WHERE d.kind=a.kind AND d.source_id=a.id);
END;

CREATE TRIGGER retract_recall_memory_hide AFTER UPDATE OF state ON memory_item
WHEN NEW.state<>'active' AND OLD.state<>NEW.state BEGIN
 DELETE FROM recall_doc WHERE (kind,source_id) IN (
  WITH RECURSIVE affected(kind,id) AS (
   SELECT 'memory',NEW.id
   UNION
   SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
    ON c.target_kind=a.kind AND c.target_id=a.id
   WHERE c.dropped_revision IS NULL AND c.view_kind IN ('summary','memory','record')
  ) SELECT kind,id FROM affected);
END;

CREATE TRIGGER retract_recall_memory_restore AFTER UPDATE OF state ON memory_item
WHEN NEW.state='active' AND OLD.state<>NEW.state BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id)
 WITH RECURSIVE affected(kind,id) AS (
  SELECT 'memory',NEW.id
  UNION
  SELECT c.view_kind,c.view_id FROM affected a JOIN cite c INDEXED BY cite_target
   ON c.target_kind=a.kind AND c.target_id=a.id
  WHERE c.dropped_revision IS NULL AND c.view_kind IN ('summary','memory','record')
 )
 SELECT a.kind,a.id FROM affected a
 WHERE EXISTS(SELECT 1 FROM recall_source s WHERE s.kind=a.kind AND s.source_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM recall_doc d WHERE d.kind=a.kind AND d.source_id=a.id);
END;
`
