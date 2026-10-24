// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerAttachmentSchema stores the metadata of a messenger attachment with
// the record revision that carried it. The bytes stay on the platform until
// the person imports them. A later revision keeps its own rows, and deleting
// the record removes the names.
const MessengerAttachmentSchema = `
CREATE TABLE messenger_attachment (
 platform TEXT NOT NULL,
 account TEXT NOT NULL,
 chat TEXT NOT NULL,
 message TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>=1),
 attachment TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '',
 mime TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL CHECK(size>=0),
 PRIMARY KEY(platform,account,chat,message,revision,attachment),
 FOREIGN KEY(platform,account,chat,message) REFERENCES messenger_record(platform,account,chat,message)
);
`
