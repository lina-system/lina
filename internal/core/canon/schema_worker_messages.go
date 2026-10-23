// SPDX-License-Identifier: Apache-2.0
package canon

const workerMessagesSchema = `
CREATE TABLE worker_message_decisions (
 task_id TEXT NOT NULL, decision_id TEXT NOT NULL,
 PRIMARY KEY(task_id, decision_id)
);
CREATE TABLE worker_message_budget (
 task_id TEXT PRIMARY KEY, used INTEGER NOT NULL CHECK(used >= 0)
);
CREATE TABLE worker_message_admissions (
 task_id TEXT NOT NULL, message_id TEXT NOT NULL, fingerprint TEXT NOT NULL,
 PRIMARY KEY(task_id, message_id)
);
`
