// SPDX-License-Identifier: Apache-2.0
package canon

// ImportSchema is registered by the optional importer host after its existing
// application migrations. Importing never migrates an open canon implicitly.
const ImportSchema = `
CREATE TABLE import_batch (
 id TEXT PRIMARY KEY NOT NULL,
 created_at TEXT NOT NULL
);
-- Unsealed event ranges live separately from every live ledger consumer.
CREATE TABLE import_event (
 id TEXT PRIMARY KEY NOT NULL,
 conversation_id TEXT NOT NULL,
 parent_id TEXT REFERENCES import_event(id),
 person_id TEXT, agent_id TEXT NOT NULL,
 speaker_kind TEXT NOT NULL CHECK(speaker_kind IN ('person','agent','external_participant','automation','forwarded','system','tool')),
 surface TEXT NOT NULL CHECK(json_valid(surface)),
 occurred_at TEXT NOT NULL, recorded_at TEXT NOT NULL,
 time_quality TEXT NOT NULL CHECK(time_quality IN ('observed','recorded','write_time_only')),
 origin TEXT NOT NULL CHECK(json_valid(origin)),
 replay_of TEXT REFERENCES import_event(id), body TEXT NOT NULL,
 material_id TEXT, material_revision INTEGER,
 FOREIGN KEY(material_id,material_revision) REFERENCES asset_revisions(asset_id,revision),
 CHECK((material_id IS NULL AND material_revision IS NULL) OR (material_id IS NOT NULL AND material_revision>0)),
 CHECK((speaker_kind='person' AND person_id IS NOT NULL) OR (speaker_kind<>'person' AND person_id IS NULL))
);
CREATE TABLE import_event_relation (
 event_id TEXT NOT NULL REFERENCES import_event(id), target_id TEXT NOT NULL REFERENCES import_event(id),
 kind TEXT NOT NULL CHECK(kind IN ('view_of','supersedes','pairs_with')), ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 PRIMARY KEY(event_id,target_id,kind,ordinal)
);
CREATE TABLE import_event_span (
 event_id TEXT NOT NULL REFERENCES import_event(id),
 start INTEGER NOT NULL CHECK(start>=0),end INTEGER NOT NULL CHECK(end>start),sensitivity TEXT NOT NULL,
 PRIMARY KEY(event_id,start,end,sensitivity)
);
CREATE TABLE import_record (
 id TEXT PRIMARY KEY NOT NULL,
 conversation_id TEXT NOT NULL,
 agent_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 stage INTEGER NOT NULL CHECK(stage BETWEEN 3 AND 8),
 body TEXT NOT NULL,
 flags TEXT NOT NULL CHECK(json_valid(flags)),
 event_id TEXT UNIQUE REFERENCES import_event(id)
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
