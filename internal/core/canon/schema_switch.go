// SPDX-License-Identifier: Apache-2.0
package canon

const WorkSwitchSchema = `
CREATE TABLE work_worker_reservations (worker_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, directory TEXT NOT NULL);
CREATE UNIQUE INDEX work_worker_task ON work_worker_reservations(task_id) WHERE task_id <> '';
CREATE TABLE work_switches (id TEXT PRIMARY KEY, record BLOB NOT NULL);
CREATE TABLE work_switch_tasks (task_id TEXT PRIMARY KEY, switch_id TEXT NOT NULL REFERENCES work_switches(id));
CREATE TABLE work_switch_events (seq INTEGER PRIMARY KEY AUTOINCREMENT, switch_id TEXT NOT NULL REFERENCES work_switches(id), record BLOB NOT NULL);
CREATE TRIGGER work_switch_events_no_update BEFORE UPDATE ON work_switch_events BEGIN SELECT RAISE(ABORT,'switch history is append only'); END;
CREATE TRIGGER work_switch_events_no_delete BEFORE DELETE ON work_switch_events BEGIN SELECT RAISE(ABORT,'switch history is append only'); END;
`
