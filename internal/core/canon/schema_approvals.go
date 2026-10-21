// SPDX-License-Identifier: Apache-2.0
package canon

const authorityApprovalsSchema = `
CREATE TABLE authority_approvals (
 approval_id TEXT PRIMARY KEY NOT NULL,
 effect_key TEXT UNIQUE NOT NULL,
 fingerprint TEXT NOT NULL,
 record BLOB NOT NULL
) WITHOUT ROWID;
CREATE TABLE authority_standing (
 grant_id TEXT PRIMARY KEY NOT NULL,
 person TEXT NOT NULL,
 record BLOB NOT NULL
) WITHOUT ROWID;
`
