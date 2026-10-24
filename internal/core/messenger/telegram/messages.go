// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// chatKey is a chat id as the messenger registry stores it: the platform's
// account-scoped kind and numeric id. It is stable across sessions.
type chatKey struct {
	kind string
	id   int64
}

func (k chatKey) String() string { return k.kind + ":" + strconv.FormatInt(k.id, 10) }

// parseChat reads a stored chat id.
func parseChat(chat string) (chatKey, bool) {
	kind, raw, ok := strings.Cut(chat, ":")
	if !ok || raw == "" {
		return chatKey{}, false
	}
	switch kind {
	case "user", "chat", "channel":
	default:
		return chatKey{}, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id == 0 {
		return chatKey{}, false
	}
	return chatKey{kind: kind, id: id}, true
}

// rememberLocked caches the entities a response carries so a message's author
// and peer can be named without another request.
func (a *Adapter) rememberLocked(users []tg.UserClass, chats []tg.ChatClass) {
	for _, item := range users {
		if user, ok := item.(*tg.User); ok && user != nil {
			a.users[user.ID] = user
			a.names["user:"+strconv.FormatInt(user.ID, 10)] = displayName(user)
		}
	}
	for _, item := range chats {
		switch chat := item.(type) {
		case *tg.Chat:
			if chat != nil {
				a.chats[chat.ID] = chat
			}
		case *tg.Channel:
			if chat != nil {
				a.channels[chat.ID] = chat
			}
		}
	}
}

// chatLocked names one dialog peer.
func (a *Adapter) chatLocked(peer tg.PeerClass) (key, name, kind string, ok bool) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		id := chatKey{kind: "user", id: p.UserID}
		user := a.users[p.UserID]
		if user == nil {
			return "", "", "", false
		}
		return id.String(), displayName(user), "direct", true
	case *tg.PeerChat:
		id := chatKey{kind: "chat", id: p.ChatID}
		chat := a.chats[p.ChatID]
		if chat == nil {
			return "", "", "", false
		}
		return id.String(), chat.Title, "group", true
	case *tg.PeerChannel:
		id := chatKey{kind: "channel", id: p.ChannelID}
		channel := a.channels[p.ChannelID]
		if channel == nil {
			return "", "", "", false
		}
		switch {
		case channel.Megagroup:
			return id.String(), channel.Title, "group", true
		case channel.Broadcast:
			return id.String(), channel.Title, "channel", true
		}
		return id.String(), channel.Title, "channel", true
	}
	return "", "", "", false
}

// peerLocked resolves a dialog's peer to the input peer a read needs.
func (a *Adapter) peerLocked(peer tg.PeerClass) (tg.InputPeerClass, bool) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		user := a.users[p.UserID]
		if user == nil {
			return nil, false
		}
		return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, true
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}, true
	case *tg.PeerChannel:
		channel := a.channels[p.ChannelID]
		if channel == nil {
			return nil, false
		}
		return &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, true
	}
	return nil, false
}

// inputPeer resolves a stored chat id to the peer a read needs.
func (a *Adapter) inputPeer(chat string) (tg.InputPeerClass, bool) {
	key, ok := parseChat(chat)
	if !ok {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch key.kind {
	case "user":
		user := a.users[key.id]
		if user == nil {
			return nil, false
		}
		return &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, true
	case "chat":
		if a.chats[key.id] == nil {
			return nil, false
		}
		return &tg.InputPeerChat{ChatID: key.id}, true
	case "channel":
		channel := a.channels[key.id]
		if channel == nil {
			return nil, false
		}
		return &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, true
	}
	return nil, false
}

func displayName(user *tg.User) string {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name != "" {
		return name
	}
	if user.Username != "" {
		return user.Username
	}
	if user.ID != 0 {
		return "Telegram user " + strconv.FormatInt(user.ID, 10)
	}
	return "Telegram user"
}

// messageLocked converts one platform message. The route comes from the
// platform's own fields: the connected account wrote it, a bot wrote it, the
// platform wrote it as a service notice, or someone else did.
func (a *Adapter) messageLocked(message *tg.Message) (messenger.Message, bool) {
	if message.PeerID == nil {
		return messenger.Message{}, false
	}
	out := messenger.Message{ID: strconv.Itoa(message.ID)}
	if message.Date > 0 {
		out.At = time.Unix(int64(message.Date), 0).UTC()
	}
	if message.EditDate > 0 {
		out.Edited = time.Unix(int64(message.EditDate), 0).UTC()
	}
	out.Text = message.Message
	out.Author = a.authorLocked(senderKey(message))
	switch {
	case message.ViaBotID != 0 || a.botLocked(message.FromID):
		out.Route = messenger.RouteAutomation
	case message.Post:
		out.Route = messenger.RouteParticipant
	case message.Out || message.FromID != nil && a.isSelfLocked(message.FromID):
		out.Route = messenger.RoutePerson
	default:
		out.Route = messenger.RouteParticipant
	}

	if reply := replyTo(message.ReplyTo); reply != "" {
		out.ReplyTo = reply
	}
	out.Attachments = a.attachmentsLocked(message)
	return out, true
}

// isSelfLocked reports whether a peer is the connected account, using the id
// the sign-in status named or the platform's self flag on a cached user.
func (a *Adapter) isSelfLocked(peer tg.PeerClass) bool {
	user, ok := peer.(*tg.PeerUser)
	if !ok {
		return false
	}
	if a.selfID != 0 && user.UserID == a.selfID {
		return true
	}
	cached, ok := a.users[user.UserID]
	return ok && cached.Self
}

func (a *Adapter) botLocked(peer tg.PeerClass) bool {
	user, ok := peer.(*tg.PeerUser)
	if !ok {
		return false
	}
	cached, ok := a.users[user.UserID]
	return ok && cached.Bot
}

func senderKey(message *tg.Message) string {
	switch from := message.FromID.(type) {
	case *tg.PeerUser:
		return "user:" + strconv.FormatInt(from.UserID, 10)
	case *tg.PeerChat:
		return "chat:" + strconv.FormatInt(from.ChatID, 10)
	case *tg.PeerChannel:
		return "channel:" + strconv.FormatInt(from.ChannelID, 10)
	}
	return ""
}

func (a *Adapter) authorLocked(key string) messenger.Author {
	if key == "" {
		return messenger.Author{ID: "telegram:unknown", Name: "Telegram"}
	}
	name := a.names[key]
	if name == "" {
		name = key
	}
	return messenger.Author{ID: key, Name: name}
}

func replyTo(class tg.MessageReplyHeaderClass) string {
	header, ok := class.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return ""
	}
	if header.ReplyToMsgID == 0 {
		return ""
	}
	return strconv.Itoa(header.ReplyToMsgID)
}

// attachmentsLocked keeps attachment metadata and the location a later
// download needs. Bytes are read only when the person asks.
func (a *Adapter) attachmentsLocked(message *tg.Message) []messenger.Attachment {
	var out []messenger.Attachment
	switch media := message.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok || photo == nil {
			return nil
		}
		id := "photo:" + strconv.FormatInt(photo.ID, 10)
		size := photoSize(photo)
		a.attachments[id] = photo.AsInputPhotoFileLocation(largestPhotoType(photo))
		out = append(out, messenger.Attachment{ID: id, Name: id, MIMEType: "image/jpeg", Size: size})
	case *tg.MessageMediaDocument:
		document, ok := media.Document.(*tg.Document)
		if !ok || document == nil {
			return nil
		}
		id := "document:" + strconv.FormatInt(document.ID, 10)
		name := documentName(document)
		a.attachments[id] = document.AsInputDocumentFileLocation("")
		out = append(out, messenger.Attachment{ID: id, Name: name, MIMEType: document.MimeType, Size: document.Size})
	}
	return out
}

func photoSize(photo *tg.Photo) int64 {
	var size int64
	for _, item := range photo.Sizes {
		if sized, ok := item.(*tg.PhotoSize); ok && int64(sized.Size) > size {
			size = int64(sized.Size)
		}
	}
	return size
}

func largestPhotoType(photo *tg.Photo) string {
	best, size := "", -1
	for _, item := range photo.Sizes {
		if sized, ok := item.(*tg.PhotoSize); ok && sized.Size > size {
			best, size = sized.Type, sized.Size
		}
	}
	return best
}

func documentName(document *tg.Document) string {
	for _, item := range document.Attributes {
		if name, ok := item.(*tg.DocumentAttributeFilename); ok && name.FileName != "" {
			return name.FileName
		}
	}
	return "document:" + strconv.FormatInt(document.ID, 10)
}

// handleUpdates subscribes the connection's update dispatcher. Only edits and
// deletions are delivered live: new messages arrive through sync, and the
// adapter never asks the platform to mark anything read.
func (a *Adapter) handleUpdates() {
	dispatcher := a.dispatcher
	if dispatcher == nil {
		return
	}
	dispatcher.OnEditMessage(func(_ context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
		a.remember(entities)
		message, ok := update.Message.(*tg.Message)
		if !ok || message == nil {
			return nil
		}
		return a.publishEdited(message)
	})
	dispatcher.OnEditChannelMessage(func(_ context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
		a.remember(entities)
		message, ok := update.Message.(*tg.Message)
		if !ok || message == nil {
			return nil
		}
		return a.publishEdited(message)
	})
	dispatcher.OnDeleteMessages(func(_ context.Context, entities tg.Entities, update *tg.UpdateDeleteMessages) error {
		a.remember(entities)
		return a.publishDeleted("", update.Messages)
	})
	dispatcher.OnDeleteChannelMessages(func(_ context.Context, entities tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
		a.remember(entities)
		return a.publishDeleted("channel:"+strconv.FormatInt(update.ChannelID, 10), update.Messages)
	})
}

func (a *Adapter) remember(entities tg.Entities) {
	a.mu.Lock()
	defer a.mu.Unlock()
	users := make([]tg.UserClass, 0, len(entities.Users))
	for _, user := range entities.Users {
		users = append(users, user)
	}
	chats := make([]tg.ChatClass, 0, len(entities.Chats)+len(entities.Channels))
	for _, chat := range entities.Chats {
		chats = append(chats, chat)
	}
	for _, channel := range entities.Channels {
		chats = append(chats, channel)
	}
	a.rememberLocked(users, chats)
}

func (a *Adapter) publishEdited(message *tg.Message) error {
	a.mu.Lock()
	converted, ok := a.messageLocked(message)
	chat, _, _, known := a.chatLocked(message.PeerID)
	a.mu.Unlock()
	if !ok || !known {
		return nil
	}
	return a.publish(messenger.Event{Chat: chat, Message: &converted})
}

// publishDeleted reports a deletion the platform pushed. A chat scope is left
// empty when the update names only message ids; the sync of the chat that owns
// them still applies the deletion through its own page.
func (a *Adapter) publishDeleted(chat string, ids []int) error {
	at := a.now()
	for _, id := range ids {
		deletion := messenger.Deletion{Message: strconv.Itoa(id), At: at}
		event := messenger.Event{Deletion: &deletion, Chat: chat}
		if chat == "" {
			event.ChatPrefixes = []string{"user:", "chat:"}
		}
		if err := a.publish(event); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) now() time.Time {
	if a.cfg.Now != nil {
		return a.cfg.Now().UTC()
	}
	return time.Now().UTC()
}
