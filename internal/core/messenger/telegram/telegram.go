// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Platform is the registry name of the person's own Telegram account.
const Platform = "telegram"

const (
	defaultPageSize = 100
	maxDialogPages  = 100
	maxFloodRetries = 3
)

var (
	ErrInvalid           = errors.New("invalid_telegram_source")
	ErrNoCredentials     = errors.New("telegram_credentials_missing")
	ErrSignUpRefused     = errors.New("telegram_account_creation_refused")
	ErrAttachmentUnknown = errors.New("telegram_attachment_unknown")
	ErrClosed            = errors.New("telegram_adapter_closed")
)

// Config opens one source: one Telegram account of one person.
type Config struct {
	Person  string
	Account string
	// Vault holds the person's api_id and api_hash and the account's session.
	Vault *secrets.Store
	// Options are extra gotd client options. Production leaves them zero; tests
	// point the client at an in-process server.
	Options telegram.Options
	// Wait sleeps for a platform retry-after. Zero sleeps on a timer.
	Wait func(context.Context, time.Duration) error
	// PageSize is the history page size, capped at the platform's 100.
	PageSize int
	// Now is the clock for deletion times. Zero uses time.Now.
	Now func() time.Time
}

// SetupSteps is the person-facing guidance shown before connecting a Telegram
// source. It names the page where the person creates their own application;
// LINA never creates one, never scripts that page and never signs in for the
// person.
var SetupSteps = [...]string{
	"Open https://my.telegram.org and create an application of your own.",
	"Copy its api_id and api_hash into LINA.",
	"Sign in with the code Telegram sends to your app, and your 2FA password if you set one.",
}

// Code describes how the platform delivered the sign-in code.
type Code struct {
	Kind    string
	Length  int
	Timeout time.Duration
	Next    string
}

// Prompt collects sign-in input from the person through the app or the TUI.
// It never stores the code or password; the adapter keeps only the session.
type Prompt interface {
	Phone(context.Context) (string, error)
	Code(context.Context, Code) (string, error)
	Password(context.Context) (string, error)
}

// PromptFuncs adapts functions to Prompt.
type PromptFuncs struct {
	PhoneFunc    func(context.Context) (string, error)
	CodeFunc     func(context.Context, Code) (string, error)
	PasswordFunc func(context.Context) (string, error)
}

func (p PromptFuncs) Phone(ctx context.Context) (string, error) {
	if p.PhoneFunc == nil {
		return "", ErrInvalid
	}
	return p.PhoneFunc(ctx)
}

func (p PromptFuncs) Code(ctx context.Context, code Code) (string, error) {
	if p.CodeFunc == nil {
		return "", ErrInvalid
	}
	return p.CodeFunc(ctx, code)
}

func (p PromptFuncs) Password(ctx context.Context) (string, error) {
	if p.PasswordFunc == nil {
		return "", ErrInvalid
	}
	return p.PasswordFunc(ctx)
}

// Adapter reads one Telegram account. It implements messenger.Adapter.
type Adapter struct {
	cfg  Config
	size int
	wait func(context.Context, time.Duration) error

	recoveryMu  sync.Mutex
	channelPts  map[int64]int
	mu          sync.Mutex
	client      *telegram.Client
	dispatcher  *tg.UpdateDispatcher
	cancel      context.CancelFunc
	done        chan struct{}
	ready       chan struct{}
	readyOnce   *sync.Once
	startErr    error
	runErr      error
	closed      bool
	liveReady   chan struct{}
	liveOnce    sync.Once
	listeners   map[int]func(messenger.Event) error
	nextID      int
	deliverMu   sync.Mutex
	users       map[int64]*tg.User
	chats       map[int64]*tg.Chat
	channels    map[int64]*tg.Channel
	names       map[string]string
	selfID      int64
	attachments map[string]tg.InputFileLocationClass
}

// New opens an adapter for one source. It does not connect until the first
// call, so a source without credentials still lists as needs sign-in.
func New(cfg Config) (*Adapter, error) {
	if cfg.Person == "" || cfg.Account == "" || cfg.Vault == nil || strings.ContainsAny(cfg.Person+cfg.Account, "\x00\r\n") {
		return nil, ErrInvalid
	}
	size := cfg.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > defaultPageSize {
		size = defaultPageSize
	}
	wait := cfg.Wait
	if wait == nil {
		wait = sleep
	}
	return &Adapter{
		cfg: cfg, size: size, wait: wait,
		liveReady:   make(chan struct{}),
		channelPts:  map[int64]int{},
		listeners:   map[int]func(messenger.Event) error{},
		users:       map[int64]*tg.User{},
		chats:       map[int64]*tg.Chat{},
		channels:    map[int64]*tg.Channel{},
		names:       map[string]string{},
		attachments: map[string]tg.InputFileLocationClass{},
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SignIn reports whether the source has a usable session. It never prompts: a
// person's sign-in runs through Authenticate from the app or the TUI.
func (a *Adapter) SignIn(ctx context.Context, source messenger.SourceID) (messenger.SignInState, error) {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return messenger.Failing, ErrInvalid
	}

	if _, _, err := a.credentials(); err != nil {
		return messenger.NeedsSignIn, nil
	}
	client, err := a.ensure(ctx)
	if err != nil {
		return messenger.Failing, err
	}
	status, err := client.Auth().Status(ctx)
	if err != nil {
		if auth.IsUnauthorized(err) {
			return messenger.NeedsSignIn, nil
		}
		return messenger.Failing, err
	}
	if !status.Authorized {
		return messenger.NeedsSignIn, nil
	}
	if status.User != nil {
		if status.User.Bot || strconv.FormatInt(status.User.ID, 10) != a.cfg.Account {
			return messenger.NeedsSignIn, ErrInvalid
		}
		a.mu.Lock()
		a.selfID = status.User.ID
		a.rememberLocked([]tg.UserClass{status.User}, nil)
		a.mu.Unlock()
	}
	return messenger.SignedIn, nil
}

// Authenticate signs the source in with a phone code and, when the account has
// one, a 2FA password. The person supplies both through prompt.
func (a *Adapter) Authenticate(ctx context.Context, prompt Prompt) error {
	if prompt == nil {
		return ErrInvalid
	}
	if _, _, err := a.credentials(); err != nil {
		return err
	}
	client, err := a.ensure(ctx)
	if err != nil {
		return err
	}
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return err
	}
	if status.Authorized {
		return nil
	}
	phone, err := prompt.Phone(ctx)
	if err != nil {
		return err
	}
	var sent tg.AuthSentCodeClass
	err = a.retry(ctx, func() error {
		var callErr error
		sent, callErr = client.Auth().SendCode(ctx, phone, auth.SendCodeOptions{})
		return callErr
	})
	if err != nil {
		return err
	}
	switch s := sent.(type) {
	case *tg.AuthSentCode:
		code, err := prompt.Code(ctx, codeOf(s))
		if err != nil {
			return err
		}
		err = a.retry(ctx, func() error {
			_, callErr := client.Auth().SignIn(ctx, phone, code, s.PhoneCodeHash)
			return callErr
		})
		if errors.Is(err, auth.ErrPasswordAuthNeeded) {
			password, promptErr := prompt.Password(ctx)
			if promptErr != nil {
				return promptErr
			}
			err = a.retry(ctx, func() error {
				_, callErr := client.Auth().Password(ctx, password)
				return callErr
			})
		}
		var signUp *auth.SignUpRequired
		if errors.As(err, &signUp) {
			return ErrSignUpRefused
		}
		return err
	case *tg.AuthSentCodeSuccess:
		return nil
	default:
		return ErrInvalid
	}
}

func codeOf(sent *tg.AuthSentCode) Code {
	code := Code{Timeout: time.Duration(sent.Timeout) * time.Second}
	switch t := sent.Type.(type) {
	case *tg.AuthSentCodeTypeApp:
		code.Kind, code.Length = "app", t.Length
	case *tg.AuthSentCodeTypeSMS:
		code.Kind, code.Length = "sms", t.Length
	case *tg.AuthSentCodeTypeCall:
		code.Kind, code.Length = "call", t.Length
	case *tg.AuthSentCodeTypeFlashCall:
		code.Kind = "flash_call"
		code.Length = len(t.Pattern)
	case *tg.AuthSentCodeTypeMissedCall:
		code.Kind, code.Length = "missed_call", t.Length
	case *tg.AuthSentCodeTypeEmailCode:
		code.Kind = "email"
		if len(t.EmailPattern) > 0 {
			code.Length = 0
		}
	case *tg.AuthSentCodeTypeSetUpEmailRequired:
		code.Kind = "email_setup"
	default:
		code.Kind = "unknown"
	}
	switch sent.NextType.(type) {
	case *tg.AuthCodeTypeSMS:
		code.Next = "sms"
	case *tg.AuthCodeTypeCall:
		code.Next = "call"
	case *tg.AuthCodeTypeFlashCall:
		code.Next = "flash_call"
	case *tg.AuthCodeTypeMissedCall:
		code.Next = "missed_call"
	case *tg.AuthCodeTypeFragmentSMS:
		code.Next = "fragment_sms"
	}
	return code
}

// retry honours the platform's FLOOD_WAIT before repeating the same request.
func (a *Adapter) retry(ctx context.Context, call func() error) error {
	for attempt := 0; ; attempt++ {
		err := call()
		wait, ok := tgerr.AsFloodWait(err)
		if !ok {
			return err
		}
		if attempt+1 >= maxFloodRetries {
			return err
		}
		if err := a.wait(ctx, wait); err != nil {
			return err
		}
	}
}

// ListChats lists the account's dialogs.
func (a *Adapter) ListChats(ctx context.Context, source messenger.SourceID) ([]messenger.Chat, error) {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return nil, ErrInvalid
	}

	api, err := a.api(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := a.SignIn(ctx, messenger.SourceID{Platform: Platform, Account: a.cfg.Account}); err != nil {
		return nil, err
	}
	var chats []messenger.Chat
	seen := map[string]bool{}
	offsetID, offsetPeer := 0, tg.InputPeerClass(&tg.InputPeerEmpty{})
	for page := 0; page < maxDialogPages; page++ {
		var response tg.MessagesDialogsClass
		err := a.retry(ctx, func() error {
			var callErr error
			response, callErr = api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
				OffsetPeer: offsetPeer, OffsetID: offsetID, Limit: a.size, Hash: 0,
			})
			return callErr
		})
		if err != nil {
			return nil, err
		}
		list, count, complete, err := a.dialogs(response)
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			if seen[item.chat.ID] {
				continue
			}
			seen[item.chat.ID] = true
			chats = append(chats, item.chat)
		}
		if complete || count < a.size {
			return chats, nil
		}
		if len(list) == 0 {
			return nil, ErrInvalid
		}
		last := list[len(list)-1]
		if last.top == 0 {
			return nil, ErrInvalid
		}
		offsetID, offsetPeer = last.top, last.peer
	}
	return nil, ErrInvalid
}

// dialogItem pairs a registry chat with the platform's pagination fields.
type dialogItem struct {
	chat messenger.Chat
	peer tg.InputPeerClass
	top  int
}

// dialogs converts one response and remembers its entities.
func (a *Adapter) dialogs(response tg.MessagesDialogsClass) ([]dialogItem, int, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var list []tg.DialogClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	complete := false
	switch v := response.(type) {
	case *tg.MessagesDialogs:
		list, users, chats = v.Dialogs, v.Users, v.Chats
		complete = true
	case *tg.MessagesDialogsSlice:
		list, users, chats = v.Dialogs, v.Users, v.Chats
	default:
		return nil, 0, true, nil
	}
	a.rememberLocked(users, chats)
	out := make([]dialogItem, 0, len(list))
	for _, item := range list {
		dialog, ok := item.(*tg.Dialog)
		if !ok || dialog == nil {
			continue
		}
		if peer, ok := dialog.Peer.(*tg.PeerChannel); ok && dialog.Pts > 0 {
			a.channelPts[peer.ChannelID] = dialog.Pts
		}
		key, name, kind, ok := a.chatLocked(dialog.Peer)
		if !ok {
			continue
		}
		peer, ok := a.peerLocked(dialog.Peer)
		if !ok {
			continue
		}
		out = append(out, dialogItem{
			chat: messenger.Chat{ID: key, Name: name, Kind: kind},
			peer: peer, top: dialog.TopMessage,
		})
	}
	return out, len(list), complete, nil
}

// Page reads one page of a chat.
func (a *Adapter) Page(ctx context.Context, source messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return messenger.Page{}, ErrInvalid
	}

	api, err := a.api(ctx)
	if err != nil {
		return messenger.Page{}, err
	}
	peer, ok := a.inputPeer(chat)
	if !ok {
		return messenger.Page{}, ErrInvalid
	}
	pos := decode(string(cursor))
	offset := 0
	if pos.phase == phaseFill {
		offset = pos.oldest
	} else {
		offset = pos.before
	}
	var response tg.MessagesMessagesClass
	err = a.retry(ctx, func() error {
		var callErr error
		response, callErr = api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer: peer, OffsetID: offset, Limit: a.size,
		})
		return callErr
	})
	if err != nil {
		return messenger.Page{}, err
	}
	raw, err := a.history(response)
	if err != nil {
		return messenger.Page{}, err
	}
	if pos.phase == phaseFill {
		return a.fillPage(pos, raw), nil
	}
	return a.steadyPage(raw, pos), nil
}

// history converts a getHistory response and remembers its entities.
func (a *Adapter) history(response tg.MessagesMessagesClass) ([]*tg.Message, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var items []tg.MessageClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	switch v := response.(type) {
	case *tg.MessagesMessages:
		items, users, chats = v.Messages, v.Users, v.Chats
	case *tg.MessagesMessagesSlice:
		items, users, chats = v.Messages, v.Users, v.Chats
	case *tg.MessagesChannelMessages:
		items, users, chats = v.Messages, v.Users, v.Chats
	}
	a.rememberLocked(users, chats)
	out := make([]*tg.Message, 0, len(items))
	for _, item := range items {
		switch message := item.(type) {
		case *tg.Message:
			if message != nil {
				out = append(out, message)
			}
		case *tg.MessageService:
			if message != nil {
				out = append(out, &tg.Message{ID: message.ID, Date: message.Date})
			}
		case *tg.MessageEmpty:
			if message != nil {
				out = append(out, &tg.Message{ID: message.ID})
			}
		}
	}
	return out, nil
}

// fillPage builds the next backward page of a first fill. The platform returns
// newest first; a page is oldest first.
func (a *Adapter) fillPage(pos position, raw []*tg.Message) messenger.Page {
	hasMore := len(raw) >= a.size
	next := position{newest: pos.newest, oldest: pos.oldest, newestAt: pos.newestAt, phase: phaseFill}
	page := messenger.Page{HasMore: hasMore}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(raw) - 1; i >= 0; i-- {
		message := raw[i]
		at := time.Unix(int64(message.Date), 0).UTC()
		if message.ID > next.newest {
			next.newest = message.ID
		}
		if next.oldest == 0 || message.ID < next.oldest {
			next.oldest = message.ID
		}
		if at.After(next.newestAt) {
			next.newestAt = at
		}
		if converted, ok := a.messageLocked(message); ok {
			page.Messages = append(page.Messages, converted)
		}
	}
	if !hasMore {
		next.phase = phaseSteady
		page.Through = next.newestAt
	}
	page.Next = messenger.Cursor(next.encode())
	return page
}

// steadyPage reads new messages after the first fill and stops at its newest
// message id, so a chat's old history is not walked again on every sync.
func (a *Adapter) steadyPage(raw []*tg.Message, pos position) messenger.Page {
	next := pos
	next.phase = phaseSteady
	next.before = 0
	page := messenger.Page{}
	minID := 0
	a.mu.Lock()
	for i := len(raw) - 1; i >= 0; i-- {
		message := raw[i]
		if minID == 0 || message.ID < minID {
			minID = message.ID
		}
		at := time.Unix(int64(message.Date), 0).UTC()
		if message.ID <= pos.newest {
			continue
		}
		if message.ID > next.pending {
			next.pending = message.ID
		}
		if at.After(next.newestAt) {
			next.newestAt = at
		}
		if converted, ok := a.messageLocked(message); ok {
			page.Messages = append(page.Messages, converted)
		}
	}
	hasMore := len(raw) >= a.size && minID > pos.newest
	if hasMore {
		next.before = minID
	}
	a.mu.Unlock()
	page.HasMore = hasMore
	if !hasMore {
		next.newest = max(next.newest, next.pending)
		next.pending = 0
		page.Through = next.newestAt
	}
	page.Next = messenger.Cursor(next.encode())
	return page
}

// Members lists a chat's members as the platform reports them.
func (a *Adapter) Members(ctx context.Context, source messenger.SourceID, chat string) ([]messenger.Author, error) {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return nil, ErrInvalid
	}

	api, err := a.api(ctx)
	if err != nil {
		return nil, err
	}
	key, ok := parseChat(chat)
	if !ok {
		return nil, ErrInvalid
	}
	a.mu.Lock()
	user := a.users[key.id]
	channel := a.channels[key.id]
	a.mu.Unlock()
	switch key.kind {
	case "user":
		if user == nil {
			return nil, ErrInvalid
		}
		return []messenger.Author{{ID: "user:" + strconv.FormatInt(user.ID, 10), Name: displayName(user)}}, nil
	case "chat":
		var response *tg.MessagesChatFull
		err = a.retry(ctx, func() error {
			var callErr error
			response, callErr = api.MessagesGetFullChat(ctx, key.id)
			return callErr
		})
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if response != nil {
			a.rememberLocked(response.Users, response.Chats)
		}
		full, ok := response.GetFullChat().(*tg.ChatFull)
		if !ok {
			return nil, ErrInvalid
		}
		participants, ok := full.GetParticipants().(*tg.ChatParticipants)
		if !ok {
			return nil, ErrInvalid
		}
		out := make([]messenger.Author, 0, len(participants.Participants))
		for _, item := range participants.Participants {
			if id, ok := chatParticipantID(item); ok {
				out = append(out, a.authorLocked("user:"+strconv.FormatInt(id, 10)))
			}
		}
		return out, nil
	case "channel":
		if channel == nil {
			return nil, ErrInvalid
		}
		var response tg.ChannelsChannelParticipantsClass
		err = a.retry(ctx, func() error {
			var callErr error
			response, callErr = api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
				Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
				Filter:  &tg.ChannelParticipantsRecent{}, Offset: 0, Limit: 200, Hash: 0,
			})
			return callErr
		})
		if err != nil {
			return nil, err
		}
		participants, ok := response.(*tg.ChannelsChannelParticipants)
		if !ok {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.rememberLocked(participants.Users, participants.Chats)
		out := make([]messenger.Author, 0, len(participants.Participants))
		for _, item := range participants.Participants {
			if id, ok := channelParticipantID(item); ok {
				out = append(out, a.authorLocked("user:"+strconv.FormatInt(id, 10)))
			}
		}
		return out, nil
	default:
		return nil, ErrInvalid
	}
}

func chatParticipantID(item tg.ChatParticipantClass) (int64, bool) {
	switch v := item.(type) {
	case *tg.ChatParticipant:
		return v.UserID, true
	case *tg.ChatParticipantAdmin:
		return v.UserID, true
	case *tg.ChatParticipantCreator:
		return v.UserID, true
	}
	return 0, false
}

func channelParticipantID(item tg.ChannelParticipantClass) (int64, bool) {
	switch v := item.(type) {
	case *tg.ChannelParticipant:
		return v.UserID, true
	case *tg.ChannelParticipantSelf:
		return v.UserID, true
	case *tg.ChannelParticipantCreator:
		return v.UserID, true
	case *tg.ChannelParticipantAdmin:
		return v.UserID, true
	case *tg.ChannelParticipantBanned:
		peer, ok := v.Peer.(*tg.PeerUser)
		if !ok {
			return 0, false
		}
		return peer.UserID, true
	}
	return 0, false
}

// Live delivers edits and deletions the platform pushes until ctx ends.
func (a *Adapter) Live(ctx context.Context, source messenger.SourceID, deliver func(messenger.Event) error) error {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return ErrInvalid
	}

	if deliver == nil {
		return ErrInvalid
	}
	if _, err := a.ensure(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	id := a.nextID
	a.nextID++
	a.listeners[id] = deliver
	a.mu.Unlock()
	a.liveOnce.Do(func() { close(a.liveReady) })
	defer func() {
		a.mu.Lock()
		delete(a.listeners, id)
		a.mu.Unlock()
	}()
	<-ctx.Done()
	return nil
}

func (a *Adapter) publish(event messenger.Event) error {
	a.deliverMu.Lock()
	a.mu.Lock()
	listeners := make([]func(messenger.Event) error, 0, len(a.listeners))
	for _, listener := range a.listeners {
		listeners = append(listeners, listener)
	}
	a.mu.Unlock()
	defer a.deliverMu.Unlock()
	for _, listener := range listeners {
		if err := listener(event); err != nil {
			return err
		}
	}
	return nil
}

// FetchAttachment reads one attachment on the person's request.
func (a *Adapter) FetchAttachment(ctx context.Context, source messenger.SourceID, chat, attachment string) (io.ReadCloser, error) {
	if source.Platform != Platform || source.Account != a.cfg.Account {
		return nil, ErrInvalid
	}

	client, err := a.ensure(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	location, ok := a.attachments[attachment]
	a.mu.Unlock()
	if !ok {
		return nil, ErrAttachmentUnknown
	}
	reader, writer := io.Pipe()
	go func() {
		_, err := client.Download(location).Stream(ctx, writer)
		_ = writer.CloseWithError(err)
	}()
	return reader, nil
}

// Capabilities describes this source. Writes arrive with the write-grant
// increment; this adapter reads only.
func (a *Adapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{
		HistoryDepth: "full",
		CursorKind:   "message_id",
		Edits:        true,
		Deletions:    true,
		LiveEvents:   true,
		Interval:     time.Minute,
	}
}

var _ messenger.Adapter = (*Adapter)(nil)

// WaitLive waits until the adapter has installed its first event subscriber.
func (a *Adapter) WaitLive(ctx context.Context) error {
	select {
	case <-a.liveReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
