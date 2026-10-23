// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerSchema adds the person's own messenger accounts as canon sources:
// the source registry, their chats with sync, learn and embed switches, the
// cursor and coverage of every complete read, the unique reference of each
// record, its revision chain for edits, and the writes LINA made in the
// person's name. A record's text is a ledger event; this schema holds only
// identity, switches and provenance.
//
// The same migration widens the ledger's event relation set for messenger
// records. ev_rel is rebuilt rather than altered because SQLite cannot change a
// CHECK constraint in place; its append-only triggers are recreated unchanged.
const MessengerSchema = `
CREATE TABLE messenger_source (
 person TEXT NOT NULL,
 platform TEXT NOT NULL,
 account TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('official','unofficial')),
 adapter TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('connected','needs_sign_in','failing','unreachable','import_only','disconnected')),
 failure TEXT NOT NULL DEFAULT '',
 learning_watermark TEXT NOT NULL DEFAULT '',
 accepted_terms TEXT NOT NULL DEFAULT '' CHECK(accepted_terms='' OR json_valid(accepted_terms)),
 connected_at TEXT NOT NULL,
 PRIMARY KEY(platform,account)
);
CREATE TABLE messenger_chat (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '',
 kind TEXT NOT NULL CHECK(kind IN ('direct','group','channel','thread')),
 first_message_at TEXT NOT NULL DEFAULT '',
 sync INTEGER NOT NULL DEFAULT 1 CHECK(sync IN (0,1)),
 learn INTEGER NOT NULL DEFAULT 1 CHECK(learn IN (0,1)),
 embed INTEGER NOT NULL DEFAULT 1 CHECK(embed IN (0,1)),
 cursor TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(platform,account,chat),
 FOREIGN KEY(platform,account) REFERENCES messenger_source(platform,account)
);
CREATE TABLE messenger_coverage (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL,
 read_from TEXT NOT NULL, read_through TEXT NOT NULL, read_at TEXT NOT NULL,
 messages INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(platform,account,chat,read_from,read_through)
);
CREATE TABLE messenger_record (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL, message TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>=0),
 event TEXT NOT NULL,
 author TEXT NOT NULL DEFAULT '',
 author_name TEXT NOT NULL DEFAULT '',
 occurred_at TEXT NOT NULL,
 edited_at TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'live' CHECK(state IN ('live','deleted','erased')),
 deleted_at TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(platform,account,chat,message)
);
CREATE INDEX messenger_record_event ON messenger_record(event);
CREATE TABLE messenger_record_revision (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL, message TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>=1),
 event TEXT NOT NULL UNIQUE,
 PRIMARY KEY(platform,account,chat,message,revision)
);
CREATE TABLE messenger_effect (
 id TEXT PRIMARY KEY NOT NULL,
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL,
 agent TEXT NOT NULL, verb TEXT NOT NULL,
 client_id TEXT NOT NULL, message TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('pending','sent','unknown','failed')),
 created_at TEXT NOT NULL, settled_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX messenger_effect_client ON messenger_effect(platform,account,chat,client_id);
CREATE INDEX messenger_effect_message ON messenger_effect(platform,account,chat,message);

DROP TRIGGER ev_rel_no_update;
DROP TRIGGER ev_rel_no_delete;
DROP TRIGGER ev_rel_no_replace;
DROP TRIGGER retract_expand_roots;
DROP TRIGGER retract_remove_roots;
DROP TRIGGER retract_expand_replay;
DROP TRIGGER retract_expand_view;
DROP VIEW retract_lineage;
CREATE TABLE ev_rel_next (
 event_id TEXT NOT NULL REFERENCES conv_event(id),
 target_id TEXT NOT NULL REFERENCES conv_event(id),
 kind TEXT NOT NULL CHECK(kind IN ('view_of','supersedes','pairs_with','replies_to','in_thread')),
 ordinal INTEGER NOT NULL DEFAULT 0 CHECK(ordinal>=0 AND (kind='view_of' OR ordinal=0)),
 PRIMARY KEY(event_id,target_id,kind,ordinal)
);
INSERT INTO ev_rel_next(event_id,target_id,kind,ordinal) SELECT event_id,target_id,kind,ordinal FROM ev_rel;
DROP TABLE ev_rel;
ALTER TABLE ev_rel_next RENAME TO ev_rel;
CREATE TRIGGER ev_rel_no_update BEFORE UPDATE ON ev_rel BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_rel_no_delete BEFORE DELETE ON ev_rel BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER ev_rel_no_replace BEFORE INSERT ON ev_rel
 WHEN EXISTS(SELECT 1 FROM ev_rel WHERE event_id=NEW.event_id AND target_id=NEW.target_id AND kind=NEW.kind AND ordinal=NEW.ordinal)
 BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;

-- The lineage projection is published as it was at its own migration; only the
-- relation table it reads changed, so its text is reproduced here unchanged.
CREATE VIEW retract_lineage AS
WITH RECURSIVE excluded(item_id,event_id) AS (
 SELECT item_id,event_id FROM retract_block WHERE event_id<>''
 UNION
 SELECT x.item_id,e.id FROM excluded x JOIN conv_event e ON e.replay_of=x.event_id
 UNION
 SELECT x.item_id,r.event_id FROM excluded x JOIN ev_rel r ON r.target_id=x.event_id AND r.kind='view_of'
)
SELECT item_id,event_id FROM excluded;
CREATE TRIGGER retract_expand_roots AFTER INSERT ON retract_block WHEN NEW.event_id<>'' BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage WHERE item_id=NEW.item_id;
END;
CREATE TRIGGER retract_remove_roots AFTER DELETE ON retract_block WHEN OLD.event_id<>'' BEGIN
 DELETE FROM retract_excluded_event WHERE item_id=OLD.item_id;
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage WHERE item_id=OLD.item_id;
END;
CREATE TRIGGER retract_expand_replay AFTER INSERT ON conv_event
WHEN NEW.replay_of IS NOT NULL AND EXISTS(SELECT 1 FROM retract_excluded_event WHERE event_id=NEW.replay_of)
BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage;
END;
CREATE TRIGGER retract_expand_view AFTER INSERT ON ev_rel
WHEN NEW.kind='view_of' AND EXISTS(SELECT 1 FROM retract_excluded_event WHERE event_id=NEW.target_id)
BEGIN
 INSERT OR IGNORE INTO retract_excluded_event SELECT * FROM retract_lineage;
END;

-- A hidden event leaves the one index in the same transaction as its removal,
-- and a restored event queues its document again. Recovery replays the same
-- targets, so a restored generation converges on this projection.
CREATE TRIGGER messenger_recall_hide AFTER INSERT ON retract_target
WHEN NEW.kind='event' AND NEW.state<>'active' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.id;
END;
CREATE TRIGGER messenger_recall_hide_update AFTER UPDATE OF state ON retract_target
WHEN NEW.kind='event' AND NEW.state<>'active' AND OLD.state='active' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.id;
 DELETE FROM recall_pending WHERE kind='event' AND source_id=NEW.id;
END;
CREATE TRIGGER messenger_recall_show AFTER UPDATE OF state ON retract_target
WHEN NEW.kind='event' AND NEW.state='active' AND OLD.state<>'active' BEGIN
 DELETE FROM recall_doc WHERE kind='event' AND source_id=NEW.id;
 INSERT OR IGNORE INTO recall_pending(kind,source_id) VALUES('event',NEW.id);
END;
`
