// SPDX-License-Identifier: Apache-2.0
package canon

// MessengerOwnerSchema records the person's own platform user id on a source.
// A bot-mode source reads a bot's servers, so the bot's own author id proves
// nothing about the person; only the numeric id the person confirmed at connect
// does, exactly as channel pairing records it.
const MessengerOwnerSchema = `
ALTER TABLE messenger_source ADD COLUMN owner TEXT NOT NULL DEFAULT '';
`
