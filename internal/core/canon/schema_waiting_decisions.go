// SPDX-License-Identifier: Apache-2.0
package canon

const WaitingDecisionsSchema = `CREATE TABLE waiting_decisions (
 source TEXT NOT NULL, item_id TEXT NOT NULL, revision TEXT NOT NULL,
 person TEXT NOT NULL, agent TEXT NOT NULL, answer TEXT NOT NULL, answered_at TEXT NOT NULL,
 PRIMARY KEY(source,item_id,revision)
);`
