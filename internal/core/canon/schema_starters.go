// SPDX-License-Identifier: Apache-2.0

package canon

// Receipts outlive trash expiry, just like the original first-run marker.
// They deliberately have no foreign key to the erasable agent row.
const startersSchema = `
CREATE TABLE agent_starters (preset TEXT PRIMARY KEY, agent_id TEXT NOT NULL UNIQUE);
CREATE TABLE agent_selection (person_id TEXT PRIMARY KEY, agent_id TEXT NOT NULL);
`
