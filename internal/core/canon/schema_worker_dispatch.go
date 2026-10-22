// SPDX-License-Identifier: Apache-2.0
package canon

const WorkerDispatchSchema = `
CREATE TABLE worker_dispatch (
 id TEXT PRIMARY KEY,
 turn_id TEXT NOT NULL,
 repository TEXT NOT NULL,
 state TEXT NOT NULL,
 record BLOB NOT NULL
);
CREATE UNIQUE INDEX worker_dispatch_repository ON worker_dispatch(repository)
 WHERE state IN ('queued','running','unknown');
`
