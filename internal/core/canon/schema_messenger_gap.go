// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerGapSchema records one coverage gap per chat until a complete read
// ends it. A gap is told to the person when it is first recorded; the row
// keeps the notice from repeating while the gap lasts
// ([docs/design/integrations.md#sync-and-coverage]).
const MessengerGapSchema = `
ALTER TABLE messenger_chat ADD COLUMN last_message TEXT NOT NULL DEFAULT '';
CREATE TABLE messenger_gap (
 platform TEXT NOT NULL, account TEXT NOT NULL, chat TEXT NOT NULL,
 detected_at TEXT NOT NULL,
 PRIMARY KEY(platform,account,chat)
);
`
