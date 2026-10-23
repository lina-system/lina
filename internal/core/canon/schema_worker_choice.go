// SPDX-License-Identifier: Apache-2.0
package canon

// Existing records retain their task/packet identity. Area reservations are
// claimed in the serialized canon writer; no prior execution is replayed.
const WorkerChoiceSchema = `
DROP INDEX worker_dispatch_repository;
CREATE INDEX worker_dispatch_repository ON worker_dispatch(repository);
CREATE TABLE worker_quota (
 worker TEXT PRIMARY KEY,
 rows BLOB NOT NULL
);
`
