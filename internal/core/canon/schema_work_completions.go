// SPDX-License-Identifier: Apache-2.0
package canon

const WorkCompletionsSchema = `
CREATE TABLE work_completions (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL,
 attempt TEXT NOT NULL,
 head TEXT NOT NULL,
 record BLOB NOT NULL,
 UNIQUE(task_id,attempt,head)
);
CREATE TABLE work_completion_claims (
 id TEXT PRIMARY KEY,
 epoch TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('claimed','handled','ignored'))
);
`
