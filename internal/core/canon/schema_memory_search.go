// SPDX-License-Identifier: Apache-2.0
package canon

const memorySearchSchema = `
INSERT INTO search_index_config VALUES('memory', 'trigram', 'hangul_bigram');
CREATE TABLE memory_search_text (
 sequence INTEGER PRIMARY KEY,
 item_id TEXT NOT NULL UNIQUE REFERENCES memory_item(id), body TEXT NOT NULL
);
CREATE VIRTUAL TABLE memory_search USING fts5(body, content='memory_search_text', content_rowid='sequence', tokenize='trigram');
CREATE TABLE memory_search_bigram (
 gram TEXT NOT NULL, item_id TEXT NOT NULL REFERENCES memory_item(id),
 PRIMARY KEY(gram, item_id)
);
CREATE INDEX memory_search_bigram_item ON memory_search_bigram(item_id);
CREATE TRIGGER memory_search_insert AFTER INSERT ON memory_search_text BEGIN
 INSERT INTO memory_search(rowid,body) VALUES(NEW.sequence,NEW.body);
 INSERT INTO memory_search_bigram(gram,item_id)
 SELECT value,NEW.item_id FROM json_each(lina_hangul_bigrams(NEW.body));
END;
CREATE TRIGGER memory_search_delete AFTER DELETE ON memory_search_text BEGIN
 INSERT INTO memory_search(memory_search,rowid,body) VALUES('delete',OLD.sequence,OLD.body);
 DELETE FROM memory_search_bigram WHERE item_id=OLD.item_id;
END;
CREATE TRIGGER memory_item_search_insert AFTER INSERT ON memory_item WHEN NEW.state='active' BEGIN
 INSERT INTO memory_search_text(sequence,item_id,body) VALUES(NEW.sequence,NEW.id,NEW.normalized);
END;
CREATE TRIGGER memory_item_search_update AFTER UPDATE OF normalized,state ON memory_item BEGIN
 DELETE FROM memory_search_text WHERE item_id=OLD.id;
 INSERT INTO memory_search_text(sequence,item_id,body)
 SELECT NEW.sequence,NEW.id,NEW.normalized WHERE NEW.state='active';
END;
CREATE TRIGGER memory_item_search_delete AFTER DELETE ON memory_item BEGIN
 DELETE FROM memory_search_text WHERE item_id=OLD.id;
END;
INSERT INTO memory_search_text(sequence,item_id,body)
 SELECT sequence,id,normalized FROM memory_item WHERE state='active';
`
