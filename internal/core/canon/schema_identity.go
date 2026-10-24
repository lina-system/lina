// SPDX-License-Identifier: Apache-2.0
package canon

// IdentitySchema adds sender identity to canon: the platform key of an author,
// the identifier and display-name observations it was seen with, the links that
// tie it to a people record, the change journal of each link, the one question a
// person is asked about an ambiguity, the artifacts a link produced, the merge
// record that keeps both people-record ids, and the requeue a repair leaves for
// extraction.
//
// The same migration records the workspace or tenant a messenger source scopes
// its ids by, so a Slack or Teams identity is keyed inside its workspace.
const IdentitySchema = `
ALTER TABLE messenger_source ADD COLUMN workspace TEXT NOT NULL DEFAULT '';

CREATE TABLE identity_sender (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 created_at TEXT NOT NULL,
 self INTEGER NOT NULL DEFAULT 0 CHECK(self IN (0,1)),
 PRIMARY KEY(platform,scope,user)
);
CREATE TABLE identity_identifier (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('phone','email')),
 value TEXT NOT NULL CHECK(length(value)>0),
 source TEXT NOT NULL DEFAULT '',
 first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
 PRIMARY KEY(platform,scope,user,kind,value,source)
);
CREATE INDEX identity_identifier_value ON identity_identifier(kind,value);
CREATE TABLE identity_name (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 name TEXT NOT NULL CHECK(length(name)>0), seen_at TEXT NOT NULL,
 PRIMARY KEY(platform,scope,user,name,seen_at)
);
CREATE TABLE identity_link (
 id TEXT PRIMARY KEY NOT NULL,
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 record TEXT NOT NULL CHECK(length(record)>0),
 class TEXT NOT NULL CHECK(class IN ('confirmed','exact','proposed')),
 maker TEXT NOT NULL,
 created_at TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>=1),
 state TEXT NOT NULL CHECK(state IN ('active','rejected','removed')),
 valid_from TEXT NOT NULL DEFAULT '', valid_through TEXT NOT NULL DEFAULT ''
);
CREATE INDEX identity_link_identity ON identity_link(platform,scope,user,state);
CREATE INDEX identity_link_record ON identity_link(record,state);
CREATE TABLE identity_link_signal (
 link TEXT NOT NULL REFERENCES identity_link(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', value TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(link,kind,source,value)
);
CREATE TABLE identity_link_change (
 link TEXT NOT NULL, revision INTEGER NOT NULL,
 action TEXT NOT NULL, class TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT '',
 record TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL, at TEXT NOT NULL,
 PRIMARY KEY(link,revision)
);
CREATE TABLE identity_question (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 reason TEXT NOT NULL, asked_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('open','answered')), answer TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(platform,scope,user)
);
CREATE TABLE identity_question_candidate (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0), record TEXT NOT NULL,
 PRIMARY KEY(platform,scope,user,ordinal)
);
CREATE TABLE identity_artifact (
 link TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('item','record_entry')), id TEXT NOT NULL,
 PRIMARY KEY(link,kind,id)
);
CREATE TABLE identity_merge (
 id TEXT PRIMARY KEY NOT NULL, person TEXT NOT NULL,
 from_record TEXT NOT NULL, into_record TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('merged','undone')),
 at TEXT NOT NULL, undone_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE identity_merge_link (
 merge TEXT NOT NULL, link TEXT NOT NULL, record TEXT NOT NULL,
 PRIMARY KEY(merge,link)
);
CREATE TABLE identity_merge_alias (
 merge TEXT NOT NULL, record TEXT NOT NULL, name TEXT NOT NULL,
 PRIMARY KEY(merge,record,name)
);
CREATE TABLE identity_merge_artifact (
 merge TEXT NOT NULL, item TEXT NOT NULL, record TEXT NOT NULL,
 PRIMARY KEY(merge,item)
);
CREATE TABLE identity_requeue (
 platform TEXT NOT NULL, scope TEXT NOT NULL DEFAULT '', user TEXT NOT NULL,
 from_at TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', queued_at TEXT NOT NULL,
 PRIMARY KEY(platform,scope,user,from_at)
);
`
