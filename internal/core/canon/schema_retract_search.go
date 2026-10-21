// SPDX-License-Identifier: Apache-2.0
package canon

// Removal uses the index owners' delete triggers to remove FTS postings too.
// Late backfills may finish consuming bytes after retraction; their writes must
// not recreate text, postings or work queues for suppressed evidence.
const retractSearchSchema = `
CREATE TRIGGER retract_purge_ledger AFTER INSERT ON retract_block WHEN NEW.event_id<>'' BEGIN
 DELETE FROM ledger_search_text WHERE event_id=NEW.event_id;
 DELETE FROM ledger_search_bigram WHERE event_id=NEW.event_id;
 DELETE FROM ledger_search_pending WHERE event_id=NEW.event_id;
END;
CREATE TRIGGER retract_recover_ledger AFTER DELETE ON retract_block
WHEN OLD.event_id<>'' AND NOT EXISTS(SELECT 1 FROM retract_block WHERE event_id=OLD.event_id)
BEGIN
 INSERT OR IGNORE INTO ledger_search_pending(event_id)
 SELECT id FROM conv_event WHERE id=OLD.event_id;
END;
CREATE TRIGGER retract_fence_ledger_text BEFORE INSERT ON ledger_search_text
WHEN EXISTS(SELECT 1 FROM retract_block WHERE event_id=NEW.event_id)
BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER retract_fence_ledger_bigram BEFORE INSERT ON ledger_search_bigram
WHEN EXISTS(SELECT 1 FROM retract_block WHERE event_id=NEW.event_id)
BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER retract_fence_ledger_pending BEFORE INSERT ON ledger_search_pending
WHEN EXISTS(SELECT 1 FROM retract_block WHERE event_id=NEW.event_id)
BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER retract_fence_memory_text BEFORE INSERT ON memory_search_text
WHEN NOT EXISTS(SELECT 1 FROM memory_item WHERE id=NEW.item_id AND state='active')
BEGIN SELECT RAISE(IGNORE); END;
CREATE TRIGGER retract_fence_memory_bigram BEFORE INSERT ON memory_search_bigram
WHEN NOT EXISTS(SELECT 1 FROM memory_item WHERE id=NEW.item_id AND state='active')
BEGIN SELECT RAISE(IGNORE); END;
`
