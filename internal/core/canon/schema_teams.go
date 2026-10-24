// SPDX-License-Identifier: Apache-2.0
package canon

// TeamsSchema adds public connection settings and sender profile evidence. No
// credentials or message bodies live in these tables; existing rows are intact.
const TeamsSchema = `
CREATE TABLE teams_connection (
 person TEXT NOT NULL, id TEXT NOT NULL, record BLOB NOT NULL,
 PRIMARY KEY(person,id)
);
CREATE TABLE messenger_author (
 platform TEXT NOT NULL, account TEXT NOT NULL, author TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(platform,account,author)
);
`
