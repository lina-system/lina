// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// Live reads pushed events from the bot token's Gateway session until ctx ends.
// The session carries the message-content intent; when Discord refuses it the
// source states that text is unavailable instead of admitting empty records.
func (a *Adapter) Live(ctx context.Context, id messenger.SourceID, deliver func(messenger.Event) error) error {
	if id != a.sourceID() {
		return messenger.ErrInvalid
	}
	if err := a.ensureChats(ctx); err != nil {
		return err
	}
	return a.listen(ctx, func(ctx context.Context, kind string, data json.RawMessage) error {
		return a.live(ctx, kind, data, deliver)
	})
}

func (a *Adapter) listed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.chats))
	for id := range a.chats {
		ids = append(ids, id)
	}
	return ids
}

// live maps a dispatch to a messenger event for a chat this source reads.
func (a *Adapter) live(ctx context.Context, kind string, data json.RawMessage, deliver func(messenger.Event) error) error {
	switch kind {
	case "MESSAGE_CREATE", "MESSAGE_UPDATE":
		var raw message
		if json.Unmarshal(data, &raw) != nil || !snowflake.MatchString(raw.ID) || !snowflake.MatchString(raw.ChannelID) {
			return messenger.ErrInvalid
		}
		if !a.knownChat(raw.ChannelID) {
			return nil
		}
		record, err := a.record(raw, a.personID(ctx))
		if err != nil {
			return err
		}
		return deliver(messenger.Event{Chat: raw.ChannelID, Message: &record})
	case "MESSAGE_DELETE", "MESSAGE_DELETE_BULK":
		var raw struct {
			ID        string   `json:"id"`
			IDs       []string `json:"ids"`
			ChannelID string   `json:"channel_id"`
		}
		if json.Unmarshal(data, &raw) != nil || !snowflake.MatchString(raw.ChannelID) {
			return messenger.ErrInvalid
		}
		if !a.knownChat(raw.ChannelID) {
			return nil
		}
		ids := raw.IDs
		if raw.ID != "" {
			ids = []string{raw.ID}
		}
		for _, id := range ids {
			if !snowflake.MatchString(id) {
				return messenger.ErrInvalid
			}
			at := time.Now().UTC()
			if err := deliver(messenger.Event{Chat: raw.ChannelID, Deletion: &messenger.Deletion{Message: id, At: at}}); err != nil {
				return err
			}
		}
	}
	return nil
}
