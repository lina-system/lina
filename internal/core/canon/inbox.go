// SPDX-License-Identifier: Apache-2.0
package canon

// InboxSchema is included in a numbered canon migration by the service that
// opens the database. Admission never creates tables or opens another writer.
const InboxSchema = `CREATE TABLE inbox (
	id INTEGER PRIMARY KEY,
	conversation TEXT NOT NULL CHECK (length(conversation) > 0),
	source TEXT NOT NULL CHECK (length(source) > 0),
	native_id TEXT NOT NULL CHECK (length(native_id) > 0),
	body BLOB NOT NULL,
	UNIQUE (conversation, source, native_id)
);`
