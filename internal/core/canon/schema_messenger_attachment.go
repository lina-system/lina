// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerAttachmentSchema keeps the attachment metadata a messenger record
// carried: the platform's attachment id, its name, its type and its size. The
// bytes stay outside LINA until the person asks for them. A record's metadata
// commits in the same transaction as the record and the chat's cursor, so the
// cursor never passes a page whose attachments were not stored. It follows the
// record's text through the trash rule: a retraction keeps it while the record
// can still be restored, and the permanent deletion of the text deletes it.
const MessengerAttachmentSchema = `
CREATE TABLE messenger_attachment (
 event TEXT NOT NULL REFERENCES conv_event(id),
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL, message TEXT NOT NULL,
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 attachment TEXT NOT NULL,
 name TEXT NOT NULL DEFAULT '',
 mime TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL DEFAULT 0 CHECK(size>=0),
 PRIMARY KEY(event,ordinal)
);
CREATE INDEX messenger_attachment_message ON messenger_attachment(platform,account,chat,message);
CREATE TRIGGER messenger_attachment_erase AFTER UPDATE ON conv_event
WHEN NEW.body='' AND EXISTS(SELECT 1 FROM retract_target WHERE kind='event' AND id=OLD.id AND state='erased')
BEGIN
 DELETE FROM messenger_attachment WHERE event=OLD.id;
END;
`
