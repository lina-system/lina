// SPDX-License-Identifier: Apache-2.0
package canon

const doneEvidenceSchema = `
CREATE TABLE done_completions (
 id TEXT PRIMARY KEY NOT NULL,
 agent_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('done','claimed')),
 record BLOB NOT NULL
);
CREATE INDEX done_completions_agent ON done_completions(agent_id,state);
CREATE VIEW agent_done_breaches AS
 SELECT agent_id,count(*) AS breaches FROM done_completions
 WHERE state='claimed' GROUP BY agent_id;
`
