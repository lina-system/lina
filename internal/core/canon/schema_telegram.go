// SPDX-License-Identifier: Apache-2.0
package canon

// TelegramSchema is part of the shared application migration catalog.
const TelegramSchema = `CREATE TABLE telegram_buttons (
 inbox_id INTEGER PRIMARY KEY REFERENCES inbox(id), bot TEXT NOT NULL,
 epoch TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1))
);
CREATE TABLE telegram_presentations (
 bot TEXT NOT NULL, chat TEXT NOT NULL, topic TEXT NOT NULL, message_id TEXT NOT NULL,
 approval_id TEXT NOT NULL REFERENCES authority_approvals(approval_id),
 PRIMARY KEY(bot,chat,message_id)
);`
