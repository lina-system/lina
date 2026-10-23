// SPDX-License-Identifier: Apache-2.0
package canon

const handoffsSchema = `
CREATE TABLE exch_origins (
 delivery_id TEXT PRIMARY KEY REFERENCES exch_msg(delivery_id),
 person TEXT NOT NULL, conversation TEXT NOT NULL,
 check_back INTEGER NOT NULL CHECK(check_back IN (0,1)),
 next_check INTEGER NOT NULL,
 artifact_kind TEXT NOT NULL DEFAULT '', artifact_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE exch_notices (
 id TEXT PRIMARY KEY, delivery_id TEXT NOT NULL REFERENCES exch_origins(delivery_id),
 reason TEXT NOT NULL CHECK(reason IN ('report','next_check')),
 delivered INTEGER NOT NULL DEFAULT 0 CHECK(delivered IN (0,1)),
 UNIQUE(delivery_id,reason)
);
CREATE TABLE artifact_changes (
 person TEXT NOT NULL, kind TEXT NOT NULL, artifact TEXT NOT NULL,
 owner TEXT NOT NULL, work_id TEXT NOT NULL, done TEXT NOT NULL,
 PRIMARY KEY(person,artifact), UNIQUE(person,work_id)
);
CREATE INDEX exch_due ON exch_origins(check_back,next_check);
`
