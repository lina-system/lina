// SPDX-License-Identifier: Apache-2.0
package canon

const ledgerSearchSchema = `
CREATE TABLE search_index_config (
 name TEXT PRIMARY KEY, tokenizer TEXT NOT NULL, fallback TEXT NOT NULL
);
INSERT INTO search_index_config VALUES('ledger', 'trigram', 'hangul_bigram');
CREATE TABLE ledger_search_text (
 event_id TEXT PRIMARY KEY REFERENCES conv_event(id), body TEXT NOT NULL
);
CREATE VIRTUAL TABLE ledger_search USING fts5(body, content='ledger_search_text', tokenize='trigram');
CREATE TRIGGER ledger_search_insert AFTER INSERT ON ledger_search_text BEGIN
 INSERT INTO ledger_search(rowid, body) VALUES(NEW.rowid, NEW.body);
END;
CREATE TRIGGER ledger_search_delete AFTER DELETE ON ledger_search_text BEGIN
 INSERT INTO ledger_search(ledger_search, rowid, body) VALUES('delete', OLD.rowid, OLD.body);
END;
CREATE TABLE ledger_search_bigram (
 gram TEXT NOT NULL, event_id TEXT NOT NULL REFERENCES conv_event(id),
 PRIMARY KEY(gram, event_id)
);
CREATE INDEX ledger_search_bigram_event ON ledger_search_bigram(event_id);
CREATE TABLE ledger_search_pending (event_id TEXT PRIMARY KEY REFERENCES conv_event(id));
INSERT INTO ledger_search_pending SELECT id FROM conv_event;
CREATE TRIGGER ledger_search_enqueue AFTER INSERT ON conv_event BEGIN
 INSERT INTO ledger_search_pending VALUES(NEW.id);
END;
`
