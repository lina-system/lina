// SPDX-License-Identifier: Apache-2.0
package canon

// Keep the word index for existing searches; Korean phrases use the same
// trigram and two-syllable fallback as conversation and memory search.
const recordsKoreanSearchSchema = `
INSERT INTO search_index_config VALUES('records_korean', 'trigram', 'hangul_bigram');
CREATE VIRTUAL TABLE records_korean_fts USING fts5(id UNINDEXED, title, note, tokenize='trigram');
CREATE TABLE records_korean_bigram (
 gram TEXT NOT NULL, id TEXT NOT NULL REFERENCES records_index(id) ON DELETE CASCADE,
 PRIMARY KEY(gram,id)
);
CREATE INDEX records_korean_bigram_id ON records_korean_bigram(id);
CREATE TRIGGER records_korean_insert AFTER INSERT ON records_text BEGIN
 INSERT INTO records_korean_fts(rowid,id,title,note) VALUES(NEW.rowid,NEW.id,NEW.title,NEW.note);
 INSERT INTO records_korean_bigram(gram,id)
 SELECT value,NEW.id FROM json_each(lina_hangul_bigrams(NEW.title || char(10) || NEW.note));
END;
CREATE TRIGGER records_korean_delete AFTER DELETE ON records_text BEGIN
 DELETE FROM records_korean_fts WHERE rowid=OLD.rowid;
 DELETE FROM records_korean_bigram WHERE id=OLD.id;
END;
INSERT INTO records_korean_fts(rowid,id,title,note) SELECT rowid,id,title,note FROM records_text;
INSERT INTO records_korean_bigram(gram,id)
 SELECT value,records_text.id FROM records_text,json_each(lina_hangul_bigrams(title || char(10) || note));
`
