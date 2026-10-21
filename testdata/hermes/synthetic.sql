-- SPDX-License-Identifier: Apache-2.0
-- Reader-mechanics fixture, not an accepted Hermes schema.
PRAGMA wal_autocheckpoint=0;
PRAGMA user_version=7;
CREATE TABLE messages (id INTEGER PRIMARY KEY, body TEXT NOT NULL);
INSERT INTO messages VALUES (1, 'synthetic committed message');
