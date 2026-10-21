// SPDX-License-Identifier: Apache-2.0
package canon

// Moirai and budget tables belong to the fixed application catalog.
const MoiraiJobsSchema = `
CREATE TABLE moirai_jobs (id TEXT PRIMARY KEY, record BLOB NOT NULL);
CREATE TABLE moirai_tasks (job TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence>0), input BLOB NOT NULL, PRIMARY KEY(job,sequence));
CREATE TABLE moirai_aux (job TEXT NOT NULL, sequence INTEGER NOT NULL, reservation TEXT NOT NULL, output BLOB, PRIMARY KEY(job,sequence));
CREATE TABLE moirai_applied (job TEXT NOT NULL, sequence INTEGER NOT NULL, proposal BLOB NOT NULL, PRIMARY KEY(job,sequence));
CREATE TABLE moirai_reports (job TEXT PRIMARY KEY, record BLOB NOT NULL);
`

const BudgetSchema = `CREATE TABLE budget_usage (
 id TEXT PRIMARY KEY, agent TEXT NOT NULL, account TEXT NOT NULL,
 provider TEXT NOT NULL, kind TEXT NOT NULL, run TEXT NOT NULL,
 reserved INTEGER NOT NULL CHECK(reserved >= 0),
 charged INTEGER NOT NULL CHECK(charged >= 0),
 settled INTEGER NOT NULL DEFAULT 0 CHECK(settled IN (0,1)),
 usage TEXT NOT NULL
)`
