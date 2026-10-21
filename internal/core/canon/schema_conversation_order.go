// SPDX-License-Identifier: Apache-2.0

package canon

// Positions contain no message content. A streamed key can precede its final
// ledger event; restoration must resolve it through canonical history first.
const conversationOrderSchema = `
CREATE TABLE conversation_order (
 position INTEGER PRIMARY KEY,
 conversation_id TEXT NOT NULL,
 item_key TEXT NOT NULL,
 UNIQUE(conversation_id,item_key)
);
`
