// SPDX-License-Identifier: Apache-2.0
package canon

const authorityEffectsSchema = `
CREATE TABLE authority_effects (
 effect_key TEXT PRIMARY KEY NOT NULL,
 fingerprint TEXT NOT NULL,
 record BLOB NOT NULL,
 running INTEGER NOT NULL CHECK(running IN (0,1))
) WITHOUT ROWID;
CREATE TABLE authority_targets (
 target TEXT PRIMARY KEY NOT NULL,
 revision TEXT NOT NULL,
 agent TEXT NOT NULL,
 active_key TEXT NOT NULL
) WITHOUT ROWID;
`
