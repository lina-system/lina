// SPDX-License-Identifier: Apache-2.0
package canon

// recallSearchSchema adds the text digest that lets recall count events with
// identical derived text as one hit, and re-queues every document so existing
// installations fill it without a search request running the drain.
const recallSearchSchema = `
ALTER TABLE recall_doc ADD COLUMN text_hash TEXT;
CREATE INDEX recall_event_text ON recall_doc(conversation,text_hash) WHERE kind='event';
INSERT OR IGNORE INTO recall_pending(kind,source_id) SELECT kind,source_id FROM recall_doc;
`
