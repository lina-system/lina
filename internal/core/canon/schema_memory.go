// SPDX-License-Identifier: Apache-2.0
package canon

const memorySchema = `
CREATE TABLE memory_item (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE, person TEXT NOT NULL, holder TEXT NOT NULL,
 subject_kind TEXT NOT NULL, subject_id TEXT NOT NULL,
 normalized TEXT NOT NULL, content TEXT NOT NULL,
 scope TEXT NOT NULL CHECK(scope IN ('local','private','never-leaves')),
 state TEXT NOT NULL CHECK(state IN ('active','retracted','trashed')),
 valid_from INTEGER NOT NULL, valid_until INTEGER NOT NULL,
 supersedes TEXT NOT NULL, standing INTEGER NOT NULL CHECK(standing IN (0,1)),
 record BLOB NOT NULL CHECK(json_valid(record))
);
CREATE INDEX memory_candidates ON memory_item(person,holder,state,scope,valid_from);
CREATE INDEX memory_duplicates ON memory_item(person,subject_kind,subject_id,holder,normalized);
CREATE INDEX memory_successors ON memory_item(supersedes);
CREATE TABLE memory_revision (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 item_id TEXT NOT NULL REFERENCES memory_item(id), revision INTEGER NOT NULL CHECK(revision>0),
 record BLOB NOT NULL CHECK(json_valid(record)),
 UNIQUE(item_id,revision)
);
`
