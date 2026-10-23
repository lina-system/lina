// SPDX-License-Identifier: Apache-2.0
package canon

// The switch window keeps all previous indexes and readers. Only identifiers
// and scope live here; FTS stores tokens, never a second copy of source text.
const recallSchema = `
CREATE TABLE recall_doc (
 id INTEGER PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('event','summary','memory','record')),
 source_id TEXT NOT NULL, revision TEXT NOT NULL,
 person TEXT NOT NULL, holder TEXT NOT NULL, conversation TEXT NOT NULL,
 occurred_at TEXT NOT NULL, speaker_kind TEXT NOT NULL, origin TEXT NOT NULL,
 replay INTEGER NOT NULL CHECK(replay IN (0,1)),
 UNIQUE(kind,source_id)
);
CREATE INDEX recall_scope ON recall_doc(person,holder,kind,occurred_at);
CREATE VIRTUAL TABLE recall_text USING fts5(body, content='', contentless_delete=1, tokenize='trigram');
CREATE VIRTUAL TABLE recall_pair USING fts5(body, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 0');
INSERT INTO search_index_config VALUES('recall','trigram','hangul_pair_words_v1');
CREATE TRIGGER recall_doc_delete AFTER DELETE ON recall_doc BEGIN
 DELETE FROM recall_text WHERE rowid=OLD.id;
 DELETE FROM recall_pair WHERE rowid=OLD.id;
END;
CREATE TABLE recall_pending (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 kind TEXT NOT NULL, source_id TEXT NOT NULL, UNIQUE(kind,source_id)
);
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
 SELECT 'memory',candidate.id,cast(json_extract(candidate.record,'$.revision') AS TEXT),candidate.person,candidate.holder,'',
 strftime('%Y-%m-%dT%H:%M:%SZ',candidate.valid_from,'unixepoch'),'',
 json_object('kind',CASE json_extract(candidate.record,'$.basis') WHEN 'imported' THEN 'import' ELSE 'native' END),0,candidate.content
 FROM memory_item candidate WHERE ` + retractMemoryCandidateVisible + `
 UNION ALL
 SELECT 'record',r.id,i.revision,r.person,'all','',coalesce(a.last_activity,''),'','{"kind":"native"}',0,r.title||char(10)||r.note
 FROM records_text r JOIN records_index i ON i.id=r.id LEFT JOIN records_activity a ON a.id=r.id;
INSERT INTO recall_pending(kind,source_id) SELECT kind,source_id FROM recall_source;
CREATE TRIGGER recall_event_insert AFTER INSERT ON conv_event BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id) VALUES('event',NEW.id);
 UPDATE recall_doc SET person=coalesce((SELECT min(o.person_id) FROM conv_event o
 WHERE o.conversation_id=NEW.conversation_id AND o.speaker_kind='person'
 HAVING count(DISTINCT o.person_id)=1),'') WHERE conversation=NEW.conversation_id AND NEW.speaker_kind='person';
END;
CREATE TRIGGER recall_event_span AFTER INSERT ON ev_span WHEN NEW.sensitivity='secret' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.event_id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.event_id;
 INSERT INTO recall_pending(kind,source_id) VALUES('event',NEW.event_id);
END;
CREATE TRIGGER recall_summary_insert AFTER INSERT ON sum_node BEGIN
 INSERT INTO recall_pending(kind,source_id) VALUES('summary',NEW.id);
END;
CREATE TRIGGER recall_summary_update AFTER UPDATE ON sum_node BEGIN
 DELETE FROM recall_doc WHERE kind='summary' AND source_id=OLD.id;
 DELETE FROM recall_pending WHERE kind='summary' AND source_id=OLD.id;
 INSERT INTO recall_pending(kind,source_id) SELECT 'summary',NEW.id WHERE NEW.status='ready';
END;
CREATE TRIGGER recall_summary_delete AFTER DELETE ON sum_node BEGIN
 DELETE FROM recall_doc WHERE kind='summary' AND source_id=OLD.id;
 DELETE FROM recall_pending WHERE kind='summary' AND source_id=OLD.id;
END;
CREATE TRIGGER recall_memory_insert AFTER INSERT ON memory_item BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id) SELECT 'memory',NEW.id WHERE NEW.state='active';
END;
CREATE TRIGGER recall_memory_update AFTER UPDATE ON memory_item BEGIN
 DELETE FROM recall_doc WHERE kind='memory' AND source_id=OLD.id;
 DELETE FROM recall_pending WHERE kind='memory' AND source_id=OLD.id;
 INSERT OR IGNORE INTO recall_pending(kind,source_id) SELECT 'memory',NEW.id WHERE NEW.state='active';
END;
CREATE TRIGGER recall_memory_delete AFTER DELETE ON memory_item BEGIN
 DELETE FROM recall_doc WHERE kind='memory' AND source_id=OLD.id;
 DELETE FROM recall_pending WHERE kind='memory' AND source_id=OLD.id;
END;
CREATE TRIGGER recall_record_insert AFTER INSERT ON records_text BEGIN
 INSERT INTO recall_pending(kind,source_id) VALUES('record',NEW.id);
END;
CREATE TRIGGER recall_record_delete AFTER DELETE ON records_text BEGIN
 DELETE FROM recall_doc WHERE kind='record' AND source_id=OLD.id;
 DELETE FROM recall_pending WHERE kind='record' AND source_id=OLD.id;
END;
-- Existing removal triggers also fence this shadow index during the switch.
-- The citation-based retraction path replaces this bridge in the next step.
CREATE TRIGGER recall_event_hidden AFTER INSERT ON retract_excluded_event BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.event_id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.event_id;
END;
CREATE TRIGGER recall_event_target_insert AFTER INSERT ON retract_target WHEN NEW.kind='event' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.id;
 INSERT INTO recall_pending(kind,source_id) SELECT 'event',NEW.id WHERE NEW.state='active';
END;
CREATE TRIGGER recall_event_target_update AFTER UPDATE ON retract_target WHEN NEW.kind='event' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.id;
 INSERT INTO recall_pending(kind,source_id) SELECT 'event',NEW.id WHERE NEW.state='active';
END;
CREATE TRIGGER recall_memory_hidden AFTER DELETE ON memory_search_text
WHEN NOT EXISTS(SELECT 1 FROM memory_item candidate WHERE candidate.id=OLD.item_id AND ` + retractMemoryCandidateVisible + `) BEGIN
 DELETE FROM recall_doc WHERE kind='memory' AND source_id=OLD.item_id;
 DELETE FROM recall_pending WHERE kind='memory' AND source_id=OLD.item_id;
 INSERT INTO recall_pending(kind,source_id) VALUES('memory',OLD.item_id);
END;
CREATE TRIGGER recall_event_restored AFTER INSERT ON ledger_search_pending BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id) VALUES('event',NEW.event_id);
END;
CREATE TRIGGER recall_memory_restored AFTER INSERT ON memory_search_text BEGIN
 DELETE FROM recall_pending WHERE kind='memory' AND source_id=NEW.item_id;
 INSERT INTO recall_pending(kind,source_id) VALUES('memory',NEW.item_id);
END;
`
