// SPDX-License-Identifier: Apache-2.0
package canon

// ImportSchema is registered by the optional importer host after its existing
// application migrations. Importing never migrates an open canon implicitly.
const ImportSchema = `
CREATE TABLE import_batch (
 id TEXT PRIMARY KEY NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE import_record (
 id TEXT PRIMARY KEY NOT NULL,
 conversation_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 stage INTEGER NOT NULL CHECK(stage BETWEEN 3 AND 8),
 body TEXT NOT NULL,
 flags TEXT NOT NULL CHECK(json_valid(flags)),
 event_id TEXT UNIQUE REFERENCES conv_event(id)
);
CREATE TABLE legacy_ref (
 system TEXT NOT NULL, instance TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
 record_id TEXT NOT NULL REFERENCES import_record(id),
 source_hash TEXT NOT NULL CHECK(length(source_hash)=64),
 batch_id TEXT NOT NULL REFERENCES import_batch(id),
 imported_at TEXT NOT NULL,
 fields TEXT NOT NULL CHECK(json_valid(fields)),
 body_column TEXT NOT NULL,
 PRIMARY KEY(system,instance,kind,key)
);
CREATE INDEX legacy_ref_record ON legacy_ref(record_id);
CREATE TABLE import_source_span (
 system TEXT NOT NULL, instance TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
 column_name TEXT NOT NULL, start INTEGER NOT NULL CHECK(start>=0),
 end INTEGER NOT NULL CHECK(end>start), sensitivity TEXT NOT NULL,
 FOREIGN KEY(system,instance,kind,key) REFERENCES legacy_ref(system,instance,kind,key),
 PRIMARY KEY(system,instance,kind,key,column_name,start,end)
);
CREATE TABLE import_link (
 record_id TEXT NOT NULL REFERENCES import_record(id),
 target_id TEXT NOT NULL REFERENCES import_record(id),
 kind TEXT NOT NULL,
 ordinal INTEGER NOT NULL,
 PRIMARY KEY(record_id,target_id,kind,ordinal)
);
CREATE TABLE import_conversation (
 id TEXT PRIMARY KEY NOT NULL REFERENCES import_record(id),
 batch_id TEXT NOT NULL REFERENCES import_batch(id),
 manifest_hash TEXT NOT NULL CHECK(length(manifest_hash)=64),
 speaker_report TEXT NOT NULL CHECK(json_valid(speaker_report)),
 hidden INTEGER NOT NULL CHECK(hidden IN (0,1))
);
`
