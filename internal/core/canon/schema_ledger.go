// SPDX-License-Identifier: Apache-2.0
package canon

const ledgerSchema = `
CREATE TABLE conv_event (
 id TEXT PRIMARY KEY NOT NULL CHECK(length(id)>0),
 conversation_id TEXT NOT NULL CHECK(length(conversation_id)>0),
 parent_id TEXT REFERENCES conv_event(id),
 person_id TEXT,
 agent_id TEXT NOT NULL CHECK(length(agent_id)>0),
 speaker_kind TEXT NOT NULL CHECK(speaker_kind IN ('person','agent','external_participant','automation','forwarded','system','tool')),
 surface TEXT NOT NULL CHECK(json_valid(surface)),
 scaffold_kind TEXT,
 scaffold_id TEXT REFERENCES conv_event(id),
 occurred_at TEXT NOT NULL,
 recorded_at TEXT NOT NULL,
 time_quality TEXT NOT NULL CHECK(time_quality IN ('observed','recorded','write_time_only')),
 origin TEXT NOT NULL CHECK(json_valid(origin)),
 replay_of TEXT REFERENCES conv_event(id),
 body TEXT,
 material_id TEXT,
 material_revision INTEGER,
 CHECK((speaker_kind='person' AND person_id IS NOT NULL AND length(person_id)>0) OR (speaker_kind<>'person' AND person_id IS NULL)),
 CHECK(scaffold_id IS NULL OR scaffold_kind IS NOT NULL),
 CHECK((body IS NOT NULL AND material_id IS NULL AND material_revision IS NULL) OR
       (body IS NULL AND material_id IS NOT NULL AND length(material_id)>0 AND material_revision IS NOT NULL AND material_revision>0))
);
CREATE INDEX conv_event_history ON conv_event(conversation_id,occurred_at);
CREATE TABLE ev_rel (
 event_id TEXT NOT NULL REFERENCES conv_event(id),
 target_id TEXT NOT NULL REFERENCES conv_event(id),
 kind TEXT NOT NULL CHECK(kind IN ('view_of','supersedes','pairs_with')),
 ordinal INTEGER NOT NULL DEFAULT 0 CHECK(ordinal>=0 AND (kind='view_of' OR ordinal=0)),
 PRIMARY KEY(event_id,target_id,kind,ordinal)
);
CREATE TABLE ev_span (
 event_id TEXT NOT NULL REFERENCES conv_event(id),
 start INTEGER NOT NULL CHECK(start>=0),
 end INTEGER NOT NULL CHECK(end>start),
 sensitivity TEXT NOT NULL CHECK(sensitivity IN ('secret','personal_id','third_party')),
 PRIMARY KEY(event_id,start,end,sensitivity)
);
CREATE TRIGGER conv_event_no_update BEFORE UPDATE ON conv_event BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER conv_event_no_delete BEFORE DELETE ON conv_event BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER conv_event_no_replace BEFORE INSERT ON conv_event WHEN EXISTS(SELECT 1 FROM conv_event WHERE id=NEW.id)
 BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_rel_no_update BEFORE UPDATE ON ev_rel BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_rel_no_delete BEFORE DELETE ON ev_rel BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_rel_no_replace BEFORE INSERT ON ev_rel
 WHEN EXISTS(SELECT 1 FROM ev_rel WHERE event_id=NEW.event_id AND target_id=NEW.target_id AND kind=NEW.kind AND ordinal=NEW.ordinal)
 BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_span_no_update BEFORE UPDATE ON ev_span BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_span_no_delete BEFORE DELETE ON ev_span BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_span_no_replace BEFORE INSERT ON ev_span
 WHEN EXISTS(SELECT 1 FROM ev_span WHERE event_id=NEW.event_id AND start=NEW.start AND end=NEW.end AND sensitivity=NEW.sensitivity)
 BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
`
