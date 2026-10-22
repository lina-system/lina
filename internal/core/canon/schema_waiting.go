// SPDX-License-Identifier: Apache-2.0
package canon

const WorkWaitingSchema = `
CREATE TABLE work_waiting_rounds (
 id TEXT PRIMARY KEY, goal TEXT NOT NULL, person TEXT NOT NULL,
 deadline INTEGER NOT NULL, record BLOB NOT NULL
);
CREATE INDEX work_waiting_deadlines ON work_waiting_rounds(deadline);
CREATE TABLE work_waiting_decisions (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, round_id TEXT NOT NULL REFERENCES work_waiting_rounds(id),
 kind TEXT NOT NULL, message_id TEXT NOT NULL, record BLOB NOT NULL,
 UNIQUE(round_id,message_id)
);
CREATE UNIQUE INDEX work_waiting_resolution ON work_waiting_decisions(round_id) WHERE kind IN ('default','answer');
CREATE TRIGGER work_waiting_rounds_no_update BEFORE UPDATE ON work_waiting_rounds BEGIN SELECT RAISE(ABORT,'waiting ledger is append only'); END;
CREATE TRIGGER work_waiting_rounds_no_delete BEFORE DELETE ON work_waiting_rounds BEGIN SELECT RAISE(ABORT,'waiting ledger is append only'); END;
CREATE TRIGGER work_waiting_decisions_no_update BEFORE UPDATE ON work_waiting_decisions BEGIN SELECT RAISE(ABORT,'waiting ledger is append only'); END;
CREATE TRIGGER work_waiting_decisions_no_delete BEFORE DELETE ON work_waiting_decisions BEGIN SELECT RAISE(ABORT,'waiting ledger is append only'); END;
`
