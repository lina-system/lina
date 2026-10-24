// SPDX-License-Identifier: Apache-2.0
package canon

// workRunLimitSchema widens the work ledger's record kinds so Core can record
// the person's run limit beside the goals it applies to. SQLite cannot alter a
// CHECK constraint, so the projection table is rebuilt in one transaction; the
// append-only event log and every existing record are copied unchanged.
const workRunLimitSchema = `
CREATE TABLE work_records_run_limit (
 kind TEXT NOT NULL CHECK(kind IN ('goal','node','action','limit')),
 id TEXT NOT NULL CHECK(length(id)>0),
 version INTEGER NOT NULL CHECK(version>0),
 body TEXT NOT NULL CHECK(json_valid(body)),
 PRIMARY KEY(kind,id)
);
INSERT INTO work_records_run_limit(kind,id,version,body) SELECT kind,id,version,body FROM work_records;
DROP TABLE work_records;
ALTER TABLE work_records_run_limit RENAME TO work_records;
`
