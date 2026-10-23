// SPDX-License-Identifier: Apache-2.0
package canon

// workEngineSchema journals committed transitions and their rebuildable projections.
// An event's body includes the resulting record changes, not external commands.
const workEngineSchema = `
CREATE TABLE work_events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE CHECK(length(id)>0),
 kind TEXT NOT NULL CHECK(length(kind)>0),
 at INTEGER NOT NULL CHECK(at>0),
 body TEXT NOT NULL CHECK(json_valid(body))
);
CREATE TABLE work_records (
 kind TEXT NOT NULL CHECK(kind IN ('goal','node','action')),
 id TEXT NOT NULL CHECK(length(id)>0),
 version INTEGER NOT NULL CHECK(version>0),
 body TEXT NOT NULL CHECK(json_valid(body)),
 PRIMARY KEY(kind,id)
);
CREATE TRIGGER work_events_no_update BEFORE UPDATE ON work_events
 BEGIN SELECT RAISE(ABORT,'work_ledger_append_only'); END;
CREATE TRIGGER work_events_no_delete BEFORE DELETE ON work_events
 BEGIN SELECT RAISE(ABORT,'work_ledger_append_only'); END;
CREATE TRIGGER work_events_no_replace BEFORE INSERT ON work_events
 WHEN EXISTS(SELECT 1 FROM work_events WHERE id=NEW.id)
 BEGIN SELECT RAISE(ABORT,'work_ledger_append_only'); END;
`

// SQLite REPLACE can delete a conflicting row without firing delete triggers.
// Guard the sequence independently of the published id guard, without rewriting
// the original migration or any event already in the journal.
const workEventSequenceSchema = `
CREATE TRIGGER work_events_no_sequence_replace BEFORE INSERT ON work_events
 WHEN EXISTS(SELECT 1 FROM work_events WHERE seq=NEW.seq)
 BEGIN SELECT RAISE(ABORT,'work_ledger_append_only'); END;
`
