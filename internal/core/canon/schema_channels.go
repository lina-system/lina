// SPDX-License-Identifier: Apache-2.0
package canon

// ChannelsSchema is part of the shared application migration catalog.
const ChannelsSchema = `
CREATE TABLE channel_connections (
 bot TEXT PRIMARY KEY, person TEXT NOT NULL, sender TEXT NOT NULL DEFAULT '',
 private_chat TEXT NOT NULL DEFAULT '', cursor TEXT NOT NULL DEFAULT '',
 pairing_hash BLOB, pairing_expires INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE channel_bindings (
 address TEXT PRIMARY KEY REFERENCES agent_bindings(channel) ON DELETE CASCADE,
 bot TEXT NOT NULL REFERENCES channel_connections(bot), chat TEXT NOT NULL, topic TEXT NOT NULL,
 conversation TEXT NOT NULL, scope BLOB NOT NULL, unreachable INTEGER NOT NULL DEFAULT 0,
 UNIQUE(bot,chat,topic)
);
CREATE TABLE channel_actions (
 id INTEGER PRIMARY KEY, address TEXT NOT NULL, person TEXT NOT NULL,
 agent TEXT NOT NULL, action TEXT NOT NULL, scope BLOB NOT NULL
);
CREATE TABLE channel_receipts (
 bot TEXT NOT NULL REFERENCES channel_connections(bot), native_id TEXT NOT NULL,
 cursor TEXT NOT NULL, address TEXT NOT NULL, agent TEXT NOT NULL, inbox_id INTEGER REFERENCES inbox(id),
 PRIMARY KEY(bot,native_id)
);
CREATE TABLE channel_outbound (
 sequence INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE,
 address TEXT NOT NULL, bot TEXT NOT NULL, chat TEXT NOT NULL, topic TEXT NOT NULL,
 agent TEXT NOT NULL, ledger_ref TEXT NOT NULL REFERENCES conv_event(id), reply_to TEXT NOT NULL, parts BLOB NOT NULL,
 next_part INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'queued'
 CHECK(state IN ('queued','sending','sent','unknown','blocked','refused')),
 retry_at INTEGER NOT NULL DEFAULT 0, message_ids BLOB NOT NULL DEFAULT '[]'
);
CREATE INDEX channel_outbound_queue ON channel_outbound(bot,chat,sequence);
`
