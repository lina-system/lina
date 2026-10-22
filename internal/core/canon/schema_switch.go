// SPDX-License-Identifier: Apache-2.0
package canon

const WorkSwitchSchema = `
CREATE TABLE work_switches (id TEXT PRIMARY KEY, record BLOB NOT NULL);
CREATE TABLE work_switch_tasks (task_id TEXT PRIMARY KEY, switch_id TEXT NOT NULL REFERENCES work_switches(id));
CREATE TABLE work_switch_events (seq INTEGER PRIMARY KEY AUTOINCREMENT, switch_id TEXT NOT NULL REFERENCES work_switches(id), record BLOB NOT NULL);
CREATE TRIGGER work_switch_events_no_update BEFORE UPDATE ON work_switch_events BEGIN SELECT RAISE(ABORT,'switch history is append only'); END;
CREATE TRIGGER work_switch_events_no_delete BEFORE DELETE ON work_switch_events BEGIN SELECT RAISE(ABORT,'switch history is append only'); END;
`
