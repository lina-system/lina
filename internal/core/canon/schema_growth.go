// SPDX-License-Identifier: Apache-2.0
package canon

const growthSchema = `
CREATE TABLE agent_growth_steps (
 agent_id TEXT NOT NULL, candidate_id TEXT NOT NULL, revision INTEGER NOT NULL,
 previous_revision INTEGER NOT NULL, content_hash TEXT NOT NULL, notice TEXT NOT NULL,
 undone_revision INTEGER,
 PRIMARY KEY(agent_id,candidate_id), UNIQUE(agent_id,revision),
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions(agent_id,revision),
 FOREIGN KEY(agent_id,previous_revision) REFERENCES agent_revisions(agent_id,revision),
 FOREIGN KEY(agent_id,undone_revision) REFERENCES agent_revisions(agent_id,revision)
);
CREATE INDEX agent_growth_suppression ON agent_growth_steps(agent_id,content_hash)
 WHERE undone_revision IS NOT NULL;
`
