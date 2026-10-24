// SPDX-License-Identifier: Apache-2.0
package canon

// DiscordSessionsSchema keeps the shared Gateway checkpoint independently of
// channel connections, so a source-only bot can resume after a reconnect.
const DiscordSessionsSchema = `CREATE TABLE discord_sessions (
 bot TEXT PRIMARY KEY, cursor TEXT NOT NULL DEFAULT ''
);`
