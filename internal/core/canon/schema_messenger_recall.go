// SPDX-License-Identifier: Apache-2.0
package canon

// Windows are summary-navigation documents with only member citations, never
// summary text. They share recall_doc and recall_vec with all other documents.
const messengerRecallSchema = `
CREATE TABLE messenger_embed_source (
 person TEXT NOT NULL, platform TEXT NOT NULL, account TEXT NOT NULL,
 PRIMARY KEY(platform,account), FOREIGN KEY(platform,account) REFERENCES messenger_source(platform,account)
);
ALTER TABLE messenger_coverage ADD COLUMN interval_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messenger_coverage ADD COLUMN window_ns INTEGER NOT NULL DEFAULT 0;
CREATE TABLE messenger_window (
 id TEXT PRIMARY KEY, platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL
);
CREATE INDEX messenger_window_chat ON messenger_window(platform,account,chat);
CREATE TABLE messenger_window_pending (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL,
 offset INTEGER NOT NULL DEFAULT 0,
 UNIQUE(platform,account,chat)
);
CREATE TRIGGER messenger_window_delete AFTER DELETE ON messenger_window BEGIN
 DELETE FROM recall_doc WHERE kind='summary' AND source_id=OLD.id;
 DELETE FROM cite WHERE view_kind='summary' AND view_id=OLD.id;
END;
INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_chat;
CREATE TRIGGER messenger_window_record_insert AFTER INSERT ON messenger_record BEGIN
 DELETE FROM messenger_window WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 DELETE FROM messenger_window_pending WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 INSERT INTO messenger_window_pending(platform,account,chat) VALUES(NEW.platform,NEW.account,NEW.chat);
END;
CREATE TRIGGER messenger_window_record_update AFTER UPDATE ON messenger_record BEGIN
 DELETE FROM messenger_window WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 DELETE FROM messenger_window_pending WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 INSERT INTO messenger_window_pending(platform,account,chat) VALUES(NEW.platform,NEW.account,NEW.chat);
END;
CREATE TRIGGER messenger_window_switch AFTER UPDATE OF embed ON messenger_chat WHEN OLD.embed<>NEW.embed BEGIN
 DELETE FROM messenger_window WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 DELETE FROM messenger_window_pending WHERE platform=NEW.platform AND account=NEW.account AND chat=NEW.chat;
 INSERT INTO messenger_window_pending(platform,account,chat) VALUES(NEW.platform,NEW.account,NEW.chat);
END;
CREATE TRIGGER messenger_window_target_insert AFTER INSERT ON retract_target WHEN NEW.kind='event' BEGIN
 DELETE FROM messenger_window WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id);
 DELETE FROM messenger_window_pending WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id);
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id;
END;
CREATE TRIGGER messenger_window_target_update AFTER UPDATE ON retract_target WHEN NEW.kind='event' BEGIN
 DELETE FROM messenger_window WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id);
 DELETE FROM messenger_window_pending WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id);
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.id;
END;
CREATE TRIGGER messenger_window_excluded AFTER INSERT ON retract_excluded_event WHEN 1 BEGIN
 DELETE FROM messenger_window WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id);
 DELETE FROM messenger_window_pending WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id);
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id;
END;
CREATE TRIGGER messenger_embed_source_grant AFTER INSERT ON messenger_embed_source BEGIN
 DELETE FROM messenger_window WHERE platform=NEW.platform AND account=NEW.account;
 DELETE FROM messenger_window_pending WHERE platform=NEW.platform AND account=NEW.account;
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_chat WHERE platform=NEW.platform AND account=NEW.account;
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
CREATE TRIGGER messenger_embed_source_revoke AFTER DELETE ON messenger_embed_source BEGIN
 DELETE FROM messenger_window WHERE platform=OLD.platform AND account=OLD.account;
 DELETE FROM messenger_window_pending WHERE platform=OLD.platform AND account=OLD.account;
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_chat WHERE platform=OLD.platform AND account=OLD.account;
 INSERT OR IGNORE INTO recall_vec_delete SELECT id FROM recall_doc;
 DELETE FROM recall_embed_pending;
 INSERT INTO recall_embed_pending(doc) SELECT id FROM recall_doc;
 UPDATE search_index_config SET generation=generation+1,ready=0 WHERE name='recall';
END;
DROP VIEW recall_embed_eligible;
CREATE VIEW recall_embed_eligible AS
 WITH RECURSIVE ancestry(doc,kind,source_id) AS (
  SELECT id,kind,source_id FROM recall_doc
  UNION
  SELECT a.doc,c.target_kind,c.target_id FROM ancestry a JOIN cite c
   ON c.view_kind=a.kind AND c.view_id=a.source_id AND c.dropped_revision IS NULL
 )
 SELECT d.* FROM recall_doc d WHERE NOT EXISTS (
  SELECT 1 FROM ancestry a WHERE a.doc=d.id AND (
   (a.kind='memory' AND EXISTS(SELECT 1 FROM memory_item m WHERE m.id=a.source_id AND
    (m.scope='never-leaves' OR (json_extract(m.record,'$.basis')='imported' AND NOT EXISTS(
     SELECT 1 FROM cite c WHERE c.view_kind='memory' AND c.view_id=m.id AND c.target_kind='event' AND c.dropped_revision IS NULL)))))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM recall_imported_event e WHERE e.id=a.source_id AND NOT EXISTS(SELECT 1 FROM recall_embed_consent k WHERE k.conversation=e.conversation_id AND e.sequence<=k.through_event)))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM cite c JOIN memory_item m ON m.id=c.view_id
    WHERE c.view_kind='memory' AND c.target_kind='event' AND c.target_id=a.source_id AND c.dropped_revision IS NULL AND m.scope='never-leaves'))
   OR (a.kind='event' AND EXISTS(SELECT 1 FROM messenger_record_revision mr JOIN messenger_chat mc
    ON mc.platform=mr.platform AND mc.account=mr.account AND mc.chat=mr.chat WHERE mr.event=a.source_id AND (mc.embed=0 OR NOT EXISTS(SELECT 1 FROM messenger_embed_source es JOIN messenger_source ms ON ms.platform=es.platform AND ms.account=es.account AND ms.person=es.person WHERE es.platform=mc.platform AND es.account=mc.account))))
  )
 );
CREATE TRIGGER messenger_window_secret AFTER INSERT ON ev_span WHEN NEW.sensitivity='secret' BEGIN
 DELETE FROM messenger_window WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id);
 DELETE FROM messenger_window_pending WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id);
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_record_revision WHERE event=NEW.event_id;
END;
CREATE TRIGGER messenger_window_restore AFTER DELETE ON retract_excluded_event WHEN 1 BEGIN
 DELETE FROM messenger_window WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=OLD.event_id);
 DELETE FROM messenger_window_pending WHERE (platform,account,chat) IN (SELECT platform,account,chat FROM messenger_record_revision WHERE event=OLD.event_id);
 INSERT INTO messenger_window_pending(platform,account,chat) SELECT platform,account,chat FROM messenger_record_revision WHERE event=OLD.event_id;
END;
`
