// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerImportSchema records a memory importer's run over a stopped
// messenger store. The imported records stay messenger records: this schema
// adds the run's retained rehearsal and report and the reference of every
// source row, keyed on its own system, instance, kind and key. A reference
// points at the messenger record the row became, so a later sync or import
// joins it and never admits the same row twice.
const MessengerImportSchema = `
CREATE TABLE messenger_import (
 batch_id TEXT PRIMARY KEY NOT NULL REFERENCES import_batch(id),
 person TEXT NOT NULL,
 system TEXT NOT NULL,
 instance TEXT NOT NULL,
 config_hash TEXT NOT NULL CHECK(length(config_hash)=64),
 spool_hash TEXT NOT NULL CHECK(length(spool_hash)=64),
 learning INTEGER NOT NULL CHECK(learning IN (0,1)),
 learning_messages INTEGER NOT NULL CHECK(learning_messages>=0),
 learning_through TEXT NOT NULL DEFAULT '',
 report TEXT NOT NULL CHECK(json_valid(report)),
 backup_generation TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL
);
CREATE TABLE messenger_legacy_ref (
 system TEXT NOT NULL, instance TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL, message TEXT NOT NULL,
 source_hash TEXT NOT NULL CHECK(length(source_hash)=64),
 batch_id TEXT NOT NULL REFERENCES import_batch(id),
 imported_at TEXT NOT NULL,
 fields TEXT NOT NULL CHECK(json_valid(fields)),
 PRIMARY KEY(system,instance,kind,key),
 FOREIGN KEY(platform,account,chat,message) REFERENCES messenger_record(platform,account,chat,message)
);
CREATE INDEX messenger_legacy_ref_record
 ON messenger_legacy_ref(platform,account,chat,message);
`
