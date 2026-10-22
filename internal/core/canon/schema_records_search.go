// SPDX-License-Identifier: Apache-2.0
package canon

// RecordsSearchSchema contains only projections of the published record files.
const RecordsSearchSchema = `
CREATE TABLE records_index_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), head TEXT NOT NULL
);
INSERT INTO records_index_state VALUES(1,'unbuilt');
CREATE TABLE records_text (
 id TEXT PRIMARY KEY NOT NULL REFERENCES records_index(id) ON DELETE CASCADE,
 person TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL,
 title TEXT NOT NULL, note TEXT NOT NULL
);
CREATE VIRTUAL TABLE records_fts USING fts5(id UNINDEXED, title, note);
CREATE TRIGGER records_text_insert AFTER INSERT ON records_text BEGIN
 INSERT INTO records_fts(rowid,id,title,note) VALUES(NEW.rowid,NEW.id,NEW.title,NEW.note);
END;
CREATE TRIGGER records_text_delete AFTER DELETE ON records_text BEGIN
 DELETE FROM records_fts WHERE rowid=OLD.rowid;
END;
CREATE TABLE records_edges (
 source TEXT NOT NULL REFERENCES records_index(id) ON DELETE CASCADE,
 relation TEXT NOT NULL CHECK(relation IN ('belongs_to','blocked_by')), target TEXT NOT NULL,
 PRIMARY KEY(source,relation,target)
);
CREATE INDEX records_edges_target ON records_edges(target,relation);
`
