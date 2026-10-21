// SPDX-License-Identifier: Apache-2.0
package canon

const importCompletionSchema = `
CREATE TABLE import_rehearsal (batch_id TEXT PRIMARY KEY REFERENCES import_batch(id), report TEXT NOT NULL CHECK(json_valid(report)));
CREATE TABLE import_seal (
 conversation_id TEXT PRIMARY KEY REFERENCES import_conversation(id),
 event_id TEXT UNIQUE NOT NULL REFERENCES conv_event(id),
 backup_generation TEXT NOT NULL DEFAULT ''
);
CREATE TABLE import_promotion (
 cluster_id TEXT PRIMARY KEY,
 confirmation_id TEXT NOT NULL CHECK(length(confirmation_id)>0),
 person_id TEXT NOT NULL,
 item_id TEXT NOT NULL,
 confirmed_at TEXT NOT NULL,
 sources TEXT NOT NULL CHECK(json_valid(sources)),
 backup_generation TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER import_range_closed BEFORE INSERT ON conv_event
 WHEN EXISTS(SELECT 1 FROM import_seal WHERE conversation_id=NEW.conversation_id)
 BEGIN SELECT RAISE(ABORT,'imported_conversation_sealed'); END;
`
