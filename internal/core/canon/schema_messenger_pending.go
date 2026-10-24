// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerPendingSchema keeps two references a messenger read can see before
// their target exists. A reply or thread member admitted before its parent
// waits in messenger_relation_pending until the parent is admitted, when the
// relation is appended to the reply's current event. A platform deletion that
// names no chat and matches no record yet waits in messenger_deletion_pending,
// so a page fetched before the deletion cannot later admit that message; its
// chat_prefixes, newline separated, limit the chats the deletion applies to.
// Both tables hold platform ids only, never text.
const MessengerPendingSchema = `
CREATE TABLE messenger_relation_pending (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL, message TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('replies_to','in_thread')),
 target TEXT NOT NULL,
 PRIMARY KEY(platform,account,chat,message,kind)
);
CREATE INDEX messenger_relation_pending_target ON messenger_relation_pending(platform,account,chat,target);
CREATE TABLE messenger_deletion_pending (
 platform TEXT NOT NULL, account TEXT NOT NULL, message TEXT NOT NULL,
 chat_prefixes TEXT NOT NULL DEFAULT '',
 deleted_at TEXT NOT NULL,
 PRIMARY KEY(platform,account,message)
);
`
