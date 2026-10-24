// SPDX-License-Identifier: Apache-2.0
package telegram_test

import (
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
)

// After a restart the adapter has no file location in memory. The fetch names
// the record, so the adapter reads that message again for a fresh location.
func TestAttachmentAfterRestartRefetchesItsMessage(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	var mu sync.Mutex
	var asked []int
	s.dispatcher.HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.MessagesGetMessagesRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		var messages []tg.MessageClass
		for _, item := range request.ID {
			id, ok := item.(*tg.InputMessageID)
			if !ok {
				continue
			}
			mu.Lock()
			asked = append(asked, id.ID)
			mu.Unlock()
			if id.ID == 5 {
				messages = append(messages, &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID}, Date: 500, Message: "with a file",
					Media: &tg.MessageMediaDocument{Document: document()}})
			}
		}
		return server.SendResult(req, &tg.MessagesMessages{Messages: messages, Users: []tg.UserClass{self, other}})
	})
	first := newAdapter(t, s, vault, nil)
	signIn(t, first)
	first.Close()

	restarted := newAdapter(t, s, vault, nil)
	if _, err := restarted.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	reader, err := restarted.FetchAttachment(t.Context(), sourceID, "user:2002", "5", "document:99")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(body) != "file" {
		t.Fatalf("attachment bytes = %q, %v", body, err)
	}
	if _, err := restarted.FetchAttachment(t.Context(), sourceID, "user:2002", "4", "document:98"); !errors.Is(err, telegram.ErrAttachmentUnknown) {
		t.Fatalf("attachment the message does not carry: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || asked[0] != 5 || asked[1] != 4 {
		t.Fatalf("messages read again = %v", asked)
	}
}

// A chat the account can no longer read reports that alone, so sync goes on
// with the account's other chats.
func TestUnreadableChatReportsUnavailable(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	private := &tg.Channel{ID: 4004, Title: "Private", Photo: &tg.ChatPhotoEmpty{}}
	private.SetAccessHash(44)
	s.dispatcher.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendResult(req, &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: private.ID}, TopMessage: 2}},
			Users:   []tg.UserClass{self, other}, Chats: []tg.ChatClass{private},
		})
	})
	s.dispatcher.HandleFunc(tg.MessagesGetHistoryRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		return server.SendErr(req, tgerr.New(406, "CHANNEL_PRIVATE"))
	})
	adapter := newAdapter(t, s, vault, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Page(t.Context(), sourceID, "channel:4004", ""); !errors.Is(err, messenger.ErrChatUnavailable) {
		t.Fatalf("private channel: %v", err)
	}
	if _, err := adapter.Page(t.Context(), sourceID, "channel:3003", ""); !errors.Is(err, messenger.ErrChatUnavailable) {
		t.Fatalf("chat missing from the dialogs: %v", err)
	}
	if _, err := adapter.Page(t.Context(), sourceID, "nonsense", ""); !errors.Is(err, telegram.ErrInvalid) {
		t.Fatalf("malformed chat id: %v", err)
	}
}
