// SPDX-License-Identifier: Apache-2.0
package canon

const mergesSchema = `
CREATE TABLE authority_merges (
 id TEXT PRIMARY KEY NOT NULL,
 person TEXT NOT NULL,
 repository TEXT NOT NULL,
 base TEXT NOT NULL,
 state TEXT NOT NULL,
 record BLOB NOT NULL
) WITHOUT ROWID;
CREATE UNIQUE INDEX authority_merge_reservation ON authority_merges(repository,base)
 WHERE state IN ('unknown','queued');
`
