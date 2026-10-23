// SPDX-License-Identifier: Apache-2.0
package canon

const steeringSchema = `
CREATE TABLE work_steering (
 id TEXT PRIMARY KEY,
 session_key TEXT NOT NULL,
 record BLOB NOT NULL
);
CREATE INDEX work_steering_session ON work_steering(session_key);
CREATE TABLE work_steering_receipts (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 message_id TEXT NOT NULL REFERENCES work_steering(id),
 record BLOB NOT NULL
);
CREATE INDEX work_steering_receipts_message ON work_steering_receipts(message_id,seq);
CREATE TRIGGER work_steering_receipts_no_update BEFORE UPDATE ON work_steering_receipts BEGIN SELECT RAISE(ABORT,'work ledger is append only'); END;
CREATE TRIGGER work_steering_receipts_no_delete BEFORE DELETE ON work_steering_receipts BEGIN SELECT RAISE(ABORT,'work ledger is append only'); END;
`
