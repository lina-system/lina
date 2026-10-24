// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerReadCoverageSchema records two facts about a read that the first
// messenger schema could not state: a record the platform served but this
// account could not decrypt, and how deep a chat's covered range is when the
// platform serves no cursor that survives a restart. A decryption failure keeps
// its record with no text, so it is never read as an empty message.
//
// The added columns carry no CHECK constraint: SQLite re-parses the schema for
// a checked ALTER TABLE ADD COLUMN, which the writer's authorizer refuses. Their
// only writer stores 0 or 1, and the default covers every existing row.
const MessengerReadCoverageSchema = `
ALTER TABLE messenger_record ADD COLUMN decryption_failed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messenger_coverage ADD COLUMN history_depth TEXT NOT NULL DEFAULT '';
ALTER TABLE messenger_coverage ADD COLUMN cursor_survives_restart INTEGER NOT NULL DEFAULT 1;
`
