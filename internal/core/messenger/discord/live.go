// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"errors"
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
	case "MESSAGE_UPDATE":
		return a.updated(ctx, data, deliver)
	case "MESSAGE_CREATE":
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

// updated maps one MESSAGE_UPDATE dispatch. Discord sends only the fields that
// changed, so an update without an author is completed from the message REST
// keeps. An update that changes neither text nor edit time, such as an embed
// refresh, is not a record change and is skipped. An update whose text the
// platform withholds never stops the shared session: the scheduled read
// reconciles that record.
func (a *Adapter) updated(ctx context.Context, data json.RawMessage, deliver func(messenger.Event) error) error {
	var fields map[string]json.RawMessage
	var raw message
	if json.Unmarshal(data, &fields) != nil || json.Unmarshal(data, &raw) != nil ||
		!snowflake.MatchString(raw.ID) || !snowflake.MatchString(raw.ChannelID) {
		return messenger.ErrInvalid
	}
	if !a.knownChat(raw.ChannelID) {
		return nil
	}
	if _, edited := fields["edited_timestamp"]; !edited {
		if _, content := fields["content"]; !content {
			// An embed, attachment or reaction refresh carries no new words.
			return nil
		}
	}
	if raw.Author.ID == "" {
		full, err := a.message(ctx, raw.ChannelID, raw.ID)
		if err != nil {
			// The message is gone or unreadable: the scheduled read owns that
			// record, and an update must not fail the shared session.
			return nil
		}
		raw = full
	}
	record, err := a.record(raw, a.personID(ctx))
	if errors.Is(err, messenger.ErrTextUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.Author.ID == "" {
		return nil
	}
	return deliver(messenger.Event{Chat: raw.ChannelID, Message: &record})
}

// message reads one message through the REST port.
func (a *Adapter) message(ctx context.Context, chat, id string) (message, error) {
	var raw message
	if err := a.client.Get(ctx, "/channels/"+chat+"/messages/"+id, nil, &raw); err != nil {
		return message{}, err
	}
	return raw, nil
}
