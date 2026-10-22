// SPDX-License-Identifier: Apache-2.0
package canon

const WorkSchema = `
CREATE TABLE work_ledger (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 task_id TEXT NOT NULL,
 event_key TEXT NOT NULL UNIQUE,
 record BLOB NOT NULL
);
CREATE INDEX work_ledger_task ON work_ledger(task_id,seq);
CREATE TRIGGER work_ledger_no_update BEFORE UPDATE ON work_ledger BEGIN SELECT RAISE(ABORT,'work ledger is append only'); END;
CREATE TRIGGER work_ledger_no_delete BEFORE DELETE ON work_ledger BEGIN SELECT RAISE(ABORT,'work ledger is append only'); END;
CREATE TABLE work_cards (task_id TEXT PRIMARY KEY, record BLOB NOT NULL);
CREATE TABLE work_active_runs (task_id TEXT PRIMARY KEY, run_id TEXT NOT NULL UNIQUE);
`
