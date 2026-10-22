// SPDX-License-Identifier: Apache-2.0
package canon

const RunsSchema = `
CREATE TABLE background_runs (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 record BLOB NOT NULL
);
`

const RunningListSchema = `CREATE TABLE running_seen(source TEXT NOT NULL, item_id TEXT NOT NULL, person TEXT NOT NULL, PRIMARY KEY(source,item_id,person));`

const LaneNoticeSchema = `
CREATE TABLE lane_notices(id TEXT PRIMARY KEY, conversation TEXT NOT NULL, record BLOB NOT NULL, delivered INTEGER NOT NULL DEFAULT 0);
CREATE INDEX lane_notices_pending ON lane_notices(conversation,delivered);
CREATE TABLE lane_notice_events(seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, conversation TEXT NOT NULL, record BLOB NOT NULL);
`
