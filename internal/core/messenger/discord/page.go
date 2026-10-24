// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// message is one Discord message with the fields a source reads.
type message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Content   string `json:"content"`
	Type      int    `json:"type"`
	Timestamp string `json:"timestamp"`
	Edited    string `json:"edited_timestamp"`
	Author    struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Global   string `json:"global_name"`
		Bot      bool   `json:"bot"`
		System   bool   `json:"system"`
	} `json:"author"`
	Referenced *struct {
		ID string `json:"id"`
	} `json:"referenced_message"`
	Attachments []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Type     string `json:"content_type"`
		Size     int64  `json:"size"`
	} `json:"attachments"`
	Stickers   []json.RawMessage `json:"sticker_items"`
	Embeds     []json.RawMessage `json:"embeds"`
	Components []json.RawMessage `json:"components"`
	Poll       *json.RawMessage  `json:"poll"`
}

// Message types that carry a person's, a bot's or a platform's words.
const (
	messageDefault      = 0
	messagePinned       = 6
	messageMemberJoin   = 7
	messageThreadStart  = 21
	messageReply        = 19
	messageThreadCreate = 18
)

// position is the chat cursor: the last message id this source read.
type position struct {
	After string `json:"after"`
}

// Page reads one page of a chat, oldest first. The first fill starts at the
// oldest message the platform keeps; a later read continues after the committed
// cursor. A page that adds nothing keeps the same cursor.
func (a *Adapter) Page(ctx context.Context, id messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if id != a.sourceID() {
		return messenger.Page{}, messenger.ErrInvalid
	}
	if !snowflake.MatchString(chat) {
		return messenger.Page{}, messenger.ErrInvalid
	}
	var at position
	if cursor != "" && json.Unmarshal([]byte(cursor), &at) != nil {
		return messenger.Page{}, messenger.ErrInvalid
	}
	after := at.After
	if after == "" {
		// Snowflake 0 is older than every message: the first fill reads forward
		// from the oldest the platform still serves.
		after = "0"
	}
	var raw []message
	query := url.Values{"after": {after}, "limit": {fmt.Sprint(pageSize)}}
	if err := a.client.Get(ctx, "/channels/"+chat+"/messages", query, &raw); err != nil {
		return messenger.Page{}, err
	}
	sort.Slice(raw, func(i, j int) bool { return older(raw[i].ID, raw[j].ID) })
	person := a.personID(ctx)
	page := messenger.Page{HasMore: len(raw) == pageSize}
	for _, m := range raw {
		record, err := a.record(m, person)
		if err != nil {
			return messenger.Page{}, err
		}
		page.Messages = append(page.Messages, record)
	}
	if len(page.Messages) > 0 {
		page.Next = mustCursor(position{After: page.Messages[len(page.Messages)-1].ID})
		page.Through = page.Messages[len(page.Messages)-1].At
	} else {
		page.Next = cursor
	}
	return page, nil
}

func mustCursor(p position) messenger.Cursor {
	data, _ := json.Marshal(p)
	return messenger.Cursor(data)
}

// record maps one platform message to a messenger record. A message whose
// words the platform withheld is refused rather than stored empty.
func (a *Adapter) record(m message, person string) (messenger.Message, error) {
	if missingText(m) {
		return messenger.Message{}, messenger.ErrTextUnavailable
	}
	record := messenger.Message{
		ID:     m.ID,
		Author: messenger.Author{ID: m.Author.ID, Name: displayName(m)},
		Route:  a.route(m, person),
		At:     parseTime(m.Timestamp),
		Edited: parseTime(m.Edited),
		Text:   m.Content,
	}
	if m.Referenced != nil {
		record.ReplyTo = m.Referenced.ID
	}
	for _, attachment := range m.Attachments {
		record.Attachments = append(record.Attachments, messenger.Attachment{ID: attachment.ID, Name: attachment.Filename, MIMEType: attachment.Type, Size: attachment.Size})
	}
	return record, nil
}

// missingText reports a message whose words the platform withheld. Discord
// answers with empty content when the privileged message-content intent is not
// granted; a message that genuinely has no words carries attachments, a
// sticker, embed, poll or components.
func missingText(m message) bool {
	if m.Content != "" {
		return false
	}
	switch m.Type {
	case messageDefault, messageReply, messageThreadStart, messageThreadCreate:
	default:
		return false
	}
	return len(m.Attachments) == 0 && len(m.Stickers) == 0 && len(m.Embeds) == 0 && len(m.Components) == 0 && m.Poll == nil
}

// route derives the speaker from the platform's own data.
func (a *Adapter) route(m message, person string) messenger.Route {
	switch {
	case m.Author.System || m.Type == messageMemberJoin || m.Type == messagePinned:
		return messenger.RouteSystem
	case m.Author.Bot || m.Author.ID == a.source.Account:
		return messenger.RouteAutomation
	case person != "" && m.Author.ID == person:
		return messenger.RoutePerson
	}
	return messenger.RouteParticipant
}

func displayName(m message) string {
	if m.Author.Global != "" {
		return m.Author.Global
	}
	return m.Author.Username
}

// older orders snowflakes: they are decimal ids whose length grows with time.
func older(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func parseTime(text string) time.Time {
	if text == "" {
		return time.Time{}
	}
	if at, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return at
	}
	return time.Time{}
}
