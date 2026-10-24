// SPDX-License-Identifier: Apache-2.0
package telegram_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// TestSignInKeepsTheSessionInTheSecretStore signs in with a phone code and a
// 2FA password, then proves the next start needs no prompt and that the
// session is only in the secret store.
var sourceID = messenger.SourceID{Platform: "telegram", Account: "1001"}

func TestSignInKeepsTheSessionInTheSecretStore(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	adapter := newAdapter(t, s, vault, nil)

	state, err := adapter.SignIn(t.Context(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if state != messenger.NeedsSignIn {
		t.Fatalf("state before sign-in = %q", state)
	}
	var askedCode telegram.Code
	prompt := telegram.PromptFuncs{
		PhoneFunc: func(context.Context) (string, error) { return personPhone, nil },
		CodeFunc: func(_ context.Context, code telegram.Code) (string, error) {
			askedCode = code
			return phoneCode, nil
		},
		PasswordFunc: func(context.Context) (string, error) { return password, nil },
	}
	if err := adapter.Authenticate(t.Context(), prompt); err != nil {
		t.Fatal(err)
	}
	if askedCode.Kind != "app" || askedCode.Length != 5 || askedCode.Next != "sms" {
		t.Fatalf("code prompt = %+v", askedCode)
	}
	state, err = adapter.SignIn(t.Context(), sourceID)
	if err != nil || state != messenger.SignedIn {
		t.Fatalf("state after sign-in = %q, %v", state, err)
	}
	stored, err := vault.GetBlob(secrets.Key{Person: "person:owner", Service: "telegram", Name: "session:1001"})
	if err != nil || !stored.Ready() {
		t.Fatalf("the session is not in the secret store: %v", err)
	}
	var wrapper struct {
		Version int
		Data    struct {
			DC      int
			AuthKey []byte
		}
	}
	if err := json.Unmarshal(stored.Bytes(), &wrapper); err != nil {
		t.Fatalf("the stored session is not an MTProto session: %v", err)
	}
	if wrapper.Version != 1 || wrapper.Data.DC == 0 || len(wrapper.Data.AuthKey) == 0 {
		t.Fatalf("the stored session has no auth key: %s", stored.Bytes()[:min(len(stored.Bytes()), 120)])
	}

	// A second adapter over the same vault resumes without a prompt.
	second := newAdapter(t, s, vault, nil)
	state, err = second.SignIn(t.Context(), sourceID)
	if err != nil || state != messenger.SignedIn {
		t.Fatalf("resumed state = %q, %v", state, err)
	}
}

// TestFirstFillPagingHonoursFloodWait reads dialogs, pages history backwards
// to the oldest message, then follows new messages through the same cursor.
func TestFirstFillPagingHonoursFloodWait(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	var waits []time.Duration
	adapter := newAdapter(t, s, vault, &waits)
	signIn(t, adapter)

	chats, err := adapter.ListChats(t.Context(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ID != "user:2002" || chats[0].Kind != "direct" || chats[0].Name != "Friend" {
		t.Fatalf("chats = %+v", chats)
	}
	if chats[1].ID != "chat:3003" || chats[1].Kind != "group" || chats[1].Name != "Team" {
		t.Fatalf("second chat = %+v", chats[1])
	}

	page, err := adapter.Page(t.Context(), sourceID, "user:2002", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != time.Second {
		t.Fatalf("flood wait = %v, want the platform's 1s", waits)
	}
	if len(page.Messages) != 5 || page.Messages[0].ID != "1" || page.Messages[4].ID != "5" {
		t.Fatalf("first fill = %+v", page.Messages)
	}
	if page.HasMore {
		t.Fatal("a short page reported more history")
	}
	if page.Messages[1].ReplyTo != "1" {
		t.Fatalf("reply link = %q", page.Messages[1].ReplyTo)
	}
	if page.Messages[2].Route != messenger.RoutePerson {
		t.Fatalf("own message route = %q", page.Messages[2].Route)
	}
	if page.Messages[0].Route != messenger.RouteParticipant {
		t.Fatalf("other message route = %q", page.Messages[0].Route)
	}
	if page.Next == "" || !strings.HasPrefix(string(page.Next), "tgc1:") {
		t.Fatalf("cursor = %q", page.Next)
	}

	// The next read is the steady one: it asks for the newest page and adds
	// nothing already read.
	page, err = adapter.Page(t.Context(), sourceID, "user:2002", page.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("a steady read returned old history: %+v", page.Messages)
	}
	s.mu.Lock()
	gets := append([]historyCall(nil), s.gets...)
	s.mu.Unlock()
	// The flood wait repeated the same request, so the fill is the second call.
	if len(gets) < 3 || gets[1].offset != 0 || gets[2].offset != 0 {
		t.Fatalf("history requests = %+v", gets)
	}
}

// TestFillPagingByMessageID pages a long chat backwards and stops at the limit.
func TestFillPagingByMessageID(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	// A page size of two forces the fill to walk backwards.
	adapter := newSizedAdapter(t, s, vault, nil, 2)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	page, err := adapter.Page(t.Context(), sourceID, "user:2002", "")
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Messages) != 2 {
		t.Fatalf("first page = %+v", page)
	}
	if page.Messages[0].ID != "4" || page.Messages[1].ID != "5" {
		t.Fatalf("fill page ids = %q %q", page.Messages[0].ID, page.Messages[1].ID)
	}
	s.mu.Lock()
	firstOffset := s.gets[0].offset
	s.mu.Unlock()
	page, err = adapter.Page(t.Context(), sourceID, "user:2002", page.Next)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	secondOffset := s.gets[len(s.gets)-1].offset
	s.mu.Unlock()
	if secondOffset != 4 {
		t.Fatalf("second fill offset = %d, want the oldest id 4 (first was %d)", secondOffset, firstOffset)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != "2" || page.Messages[1].ID != "3" {
		t.Fatalf("second fill page = %+v", page.Messages)
	}
}

// TestLiveEditAndDeletionUpdate delivers an edit and a deletion the platform
// pushed, and proves both are reported with their chat.
func TestLiveEditAndDeletionUpdate(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	adapter := newAdapter(t, s, vault, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Page(t.Context(), sourceID, "user:2002", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := make(chan messenger.Event, 4)
	go func() {
		_ = adapter.Live(ctx, sourceID, func(event messenger.Event) error {
			events <- event
			return nil
		})
	}()
	awaitSession(t, s)

	edited := &tg.Message{
		ID: 2, PeerID: &tg.PeerUser{UserID: other.ID}, FromID: &tg.PeerUser{UserID: other.ID},
		Date: 200, Message: "second, edited", EditDate: 500,
	}
	if err := s.tgServer.SendUpdates(ctx, s.session, &tg.UpdateEditMessage{Message: edited, Pts: 2, PtsCount: 1}); err != nil {
		t.Fatal(err)
	}
	event := nextEvent(t, events)
	if event.Message == nil || event.Chat != "user:2002" || event.Message.Text != "second, edited" || event.Message.Edited.IsZero() {
		t.Fatalf("edit event = %+v", event)
	}

	if err := s.tgServer.SendUpdates(ctx, s.session, &tg.UpdateDeleteMessages{Messages: []int{1}, Pts: 3, PtsCount: 1}); err != nil {
		t.Fatal(err)
	}
	event = nextEvent(t, events)
	if event.Deletion == nil || event.Deletion.Message != "1" || event.Chat != "" {
		t.Fatalf("deletion event = %+v", event)
	}
	if !event.Deletion.At.Equal(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("deletion time = %s", event.Deletion.At)
	}
}

// TestReadsNeverTouchReadStateOrPresence asserts the adapter's method set: it
// never marks a message read, shows presence or sends typing.
func TestReadsNeverTouchReadStateOrPresence(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	adapter := newAdapter(t, s, vault, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	page, err := adapter.Page(t.Context(), sourceID, "user:2002", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Members(t.Context(), sourceID, "chat:3003"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Page(t.Context(), sourceID, "user:2002", page.Next); err != nil {
		t.Fatal(err)
	}
	forbidden := map[uint32]string{
		tg.MessagesReadHistoryRequestTypeID:         "messages.readHistory",
		tg.MessagesReadMentionsRequestTypeID:        "messages.readMentions",
		tg.MessagesReadReactionsRequestTypeID:       "messages.readReactions",
		tg.MessagesReadMessageContentsRequestTypeID: "messages.readMessageContents",
		tg.ChannelsReadHistoryRequestTypeID:         "channels.readHistory",
		tg.AccountUpdateStatusRequestTypeID:         "account.updateStatus",
		tg.MessagesSetTypingRequestTypeID:           "messages.setTyping",
		tg.AccountUpdateNotifySettingsRequestTypeID: "account.updateNotifySettings",
	}
	for _, id := range s.invokedRequests() {
		if name, ok := forbidden[id]; ok {
			t.Fatalf("the adapter sent %s (%#x)", name, id)
		}
	}
}

// TestAttachmentBytesStayUntilAsked reads a message's attachment metadata from
// sync and fetches its bytes only on request.
func TestAttachmentBytesStayUntilAsked(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	adapter := newAdapter(t, s, vault, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	page, err := adapter.Page(t.Context(), sourceID, "user:2002", "")
	if err != nil {
		t.Fatal(err)
	}
	last := page.Messages[len(page.Messages)-1]
	if len(last.Attachments) != 1 {
		t.Fatalf("attachments = %+v", last.Attachments)
	}
	attachment := last.Attachments[0]
	if attachment.ID != "document:99" || attachment.Name != "notes.txt" || attachment.MIMEType != "text/plain" || attachment.Size != 4 {
		t.Fatalf("attachment = %+v", attachment)
	}
	if _, err := adapter.FetchAttachment(t.Context(), sourceID, "user:2002", "document:404"); !errors.Is(err, telegram.ErrAttachmentUnknown) {
		t.Fatalf("unknown attachment error = %v", err)
	}
	reader, err := adapter.FetchAttachment(t.Context(), sourceID, "user:2002", attachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body := make([]byte, 8)
	n, err := reader.Read(body)
	if err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
	if string(body[:n]) != "file" {
		t.Fatalf("attachment bytes = %q", body[:n])
	}
}

// TestMembersListsBothChatKinds lists a direct chat's single member and a basic
// group's participants.
func TestMembersListsBothChatKinds(t *testing.T) {
	t.Parallel()
	s := newServer(t)
	vault := newVault(t)
	adapter := newAdapter(t, s, vault, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), sourceID); err != nil {
		t.Fatal(err)
	}
	members, err := adapter.Members(t.Context(), sourceID, "user:2002")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != "user:2002" || members[0].Name != "Friend" {
		t.Fatalf("direct members = %+v", members)
	}
	members, err = adapter.Members(t.Context(), sourceID, "chat:3003")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].ID != "user:1001" || members[1].ID != "user:2002" {
		t.Fatalf("group members = %+v", members)
	}
}

// TestSignInRefusesMissingCredentials keeps the source at needs sign-in until
// the person supplies their own api_id.
func TestSignInRefusesMissingCredentials(t *testing.T) {
	t.Parallel()
	vault := newVault(t)
	adapter, err := telegram.New(telegram.Config{Person: "person:owner", Account: "1001", Vault: vault})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	state, err := adapter.SignIn(t.Context(), sourceID)
	if err != nil || state != messenger.NeedsSignIn {
		t.Fatalf("state = %q, %v", state, err)
	}
	if err := adapter.Authenticate(t.Context(), telegram.PromptFuncs{}); !errors.Is(err, telegram.ErrNoCredentials) {
		t.Fatalf("authenticate error = %v", err)
	}
}

func signIn(t *testing.T, adapter *telegram.Adapter) {
	t.Helper()
	err := adapter.Authenticate(t.Context(), telegram.PromptFuncs{
		PhoneFunc:    func(context.Context) (string, error) { return personPhone, nil },
		CodeFunc:     func(context.Context, telegram.Code) (string, error) { return phoneCode, nil },
		PasswordFunc: func(context.Context) (string, error) { return password, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
}

func awaitSession(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ready := s.tgServer != nil && len(s.session.AuthKey.ID) > 0
		s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the client never established a session")
}

func nextEvent(t *testing.T, events <-chan messenger.Event) messenger.Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("no live event")
		return messenger.Event{}
	}
}
