// SPDX-License-Identifier: Apache-2.0
package canon

// A document's person is the only person who has spoken in its conversation, or
// empty when nobody has or more than one person has. conv_event is append-only,
// so that set only grows. recall_conversation_scope stores the value after the
// first person event: the person id, or empty once a second person speaks. No
// row means no person has spoken. A later person event rewrites indexed
// documents only when the stored value changes. A repeat from that person is a
// primary-key read and does not touch recall_doc. recall_conversation indexes
// the document lookup. Published migration 58 keeps its original trigger; this
// migration replaces it.
const recallScopeSchema = `
CREATE TABLE recall_conversation_scope (
 conversation TEXT PRIMARY KEY,
 person TEXT NOT NULL
);
CREATE INDEX recall_conversation ON recall_doc(conversation, person);
INSERT INTO recall_conversation_scope(conversation, person)
SELECT conversation_id, CASE WHEN count(DISTINCT person_id)=1 THEN min(person_id) ELSE '' END
FROM conv_event WHERE speaker_kind='person' GROUP BY conversation_id;
UPDATE recall_doc SET person=coalesce((SELECT person FROM recall_conversation_scope s WHERE s.conversation=recall_doc.conversation), '')
WHERE conversation<>''
AND person<>coalesce((SELECT person FROM recall_conversation_scope s WHERE s.conversation=recall_doc.conversation), '');
DROP TRIGGER recall_event_insert;
CREATE TRIGGER recall_event_insert AFTER INSERT ON conv_event BEGIN
 INSERT OR IGNORE INTO recall_pending(kind,source_id) VALUES('event',NEW.id);
END;
CREATE TRIGGER recall_event_scope AFTER INSERT ON conv_event
WHEN NEW.speaker_kind='person' AND (
 NOT EXISTS(SELECT 1 FROM recall_conversation_scope WHERE conversation=NEW.conversation_id)
 OR EXISTS(SELECT 1 FROM recall_conversation_scope WHERE conversation=NEW.conversation_id AND person<>'' AND person<>NEW.person_id)
) BEGIN
 INSERT INTO recall_conversation_scope(conversation, person) VALUES(NEW.conversation_id, NEW.person_id)
 ON CONFLICT(conversation) DO UPDATE SET person=''
 WHERE recall_conversation_scope.person<>'' AND recall_conversation_scope.person<>NEW.person_id;
 UPDATE recall_doc SET person=(SELECT person FROM recall_conversation_scope WHERE conversation=NEW.conversation_id)
 WHERE conversation=NEW.conversation_id
 AND person<>(SELECT person FROM recall_conversation_scope WHERE conversation=NEW.conversation_id);
END;
`
