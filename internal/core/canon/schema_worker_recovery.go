// SPDX-License-Identifier: Apache-2.0
package canon

const workerRecoverySchema = `
CREATE TABLE worker_recovery (
 id TEXT PRIMARY KEY,
 record BLOB NOT NULL
);
`
