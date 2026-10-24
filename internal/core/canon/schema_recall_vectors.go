// SPDX-License-Identifier: Apache-2.0
package canon

const recallVectorsSchema = `
ALTER TABLE search_index_config ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE search_index_config ADD COLUMN dimension INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_index_config ADD COLUMN generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_index_config ADD COLUMN ready INTEGER NOT NULL DEFAULT 0;
ALTER TABLE search_index_config ADD COLUMN endpoint TEXT NOT NULL DEFAULT '';
UPDATE search_index_config SET model='BAAI/bge-m3',dimension=1024 WHERE name='recall';
CREATE VIRTUAL TABLE recall_vec USING vec0(embedding float[1024] distance_metric=cosine);
CREATE TABLE recall_vec_delete (doc INTEGER PRIMARY KEY);
CREATE TABLE recall_embed_pending (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 doc INTEGER NOT NULL UNIQUE REFERENCES recall_doc(id) ON DELETE CASCADE
);
CREATE TABLE recall_embed_consent (
 conversation TEXT PRIMARY KEY, through_event INTEGER NOT NULL, granted_at TEXT NOT NULL
);
CREATE TRIGGER recall_embed_insert AFTER INSERT ON recall_doc BEGIN
 INSERT INTO recall_embed_pending(doc) VALUES(NEW.id);
END;
CREATE TRIGGER recall_embed_delete AFTER DELETE ON recall_doc BEGIN
 INSERT OR IGNORE INTO recall_vec_delete VALUES(OLD.id);
 DELETE FROM recall_embed_pending WHERE doc=OLD.id;
END;
INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
CREATE TRIGGER recall_embed_grant AFTER INSERT ON recall_embed_consent BEGIN
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
CREATE TRIGGER recall_embed_extend AFTER UPDATE ON recall_embed_consent BEGIN
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
CREATE TRIGGER recall_embed_revoke AFTER DELETE ON recall_embed_consent BEGIN
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
-- Recursively follow current citations. Consent and never-leaves sensitivity
-- protect derived summaries/items/records as well as the event itself.
CREATE VIEW recall_imported_event AS
 SELECT e.id,e.conversation_id,e.rowid AS sequence FROM conv_event e WHERE json_extract(e.origin,'$.kind')='import'
 OR EXISTS(SELECT 1 FROM messenger_record_revision mr JOIN messenger_legacy_ref l
 ON l.platform=mr.platform AND l.account=mr.account AND l.chat=mr.chat AND l.message=mr.message WHERE mr.event=e.id);
CREATE VIEW recall_embed_eligible AS
 WITH RECURSIVE edges(view_kind,view_id,target_kind,target_id) AS (
  SELECT view_kind,view_id,target_kind,target_id FROM cite WHERE dropped_revision IS NULL
  UNION ALL
  SELECT 'event',id,'event',replay_of FROM conv_event WHERE replay_of IS NOT NULL
  UNION ALL
  SELECT 'event',event_id,'event',target_id FROM ev_rel WHERE kind='view_of'
 ), ancestry(doc,kind,source_id) AS (
  SELECT id,kind,source_id FROM recall_doc
  UNION
  SELECT a.doc,e.target_kind,e.target_id FROM ancestry a JOIN edges e
   ON e.view_kind=a.kind AND e.view_id=a.source_id
 )
 SELECT d.* FROM recall_doc d WHERE NOT EXISTS (
  SELECT 1 FROM ancestry a WHERE a.doc=d.id AND (
   (a.kind='memory' AND EXISTS(SELECT 1 FROM memory_item m WHERE m.id=a.source_id AND
    (m.scope='never-leaves' OR (json_extract(m.record,'$.basis')='imported' AND NOT EXISTS(
     SELECT 1 FROM cite c WHERE c.view_kind='memory' AND c.view_id=m.id AND c.target_kind='event' AND c.dropped_revision IS NULL)))))
   OR (a.kind='event' AND (d.kind<>'event' OR d.source_id<>a.source_id)
    AND EXISTS(SELECT 1 FROM ev_span span WHERE span.event_id=a.source_id AND span.sensitivity='secret'))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM recall_imported_event e WHERE e.id=a.source_id AND NOT EXISTS(SELECT 1 FROM recall_embed_consent k WHERE k.conversation=e.conversation_id AND e.sequence<=k.through_event)))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM cite c JOIN memory_item m ON m.id=c.view_id
    WHERE c.view_kind='memory' AND c.target_kind='event' AND c.target_id=a.source_id AND c.dropped_revision IS NULL AND m.scope='never-leaves'))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM messenger_record_revision mr JOIN messenger_chat mc
    ON mc.platform=mr.platform AND mc.account=mr.account AND mc.chat=mr.chat WHERE mr.event=a.source_id AND mc.embed=0))
  )
 );
CREATE TRIGGER recall_embed_chat_switch AFTER UPDATE OF embed ON messenger_chat WHEN OLD.embed<>NEW.embed BEGIN
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
`
