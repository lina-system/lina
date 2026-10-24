// SPDX-License-Identifier: Apache-2.0

// Package addon speaks the messenger add-on's line protocol and supervises its
// process. The add-on is LINA's own TypeScript program on the person's Node.js,
// running the pinned agent-messenger SDK as a child of Core inside the OS
// sandbox ([docs/design/integrations.md#the-messenger-add-on]).
//
// The add-on offers read operations only. A frame whose shape this package does
// not know refuses the batch and leaves the source failing with
// ErrUpstreamChanged, so the cursor never moves past a message Core could not
// read. One process runs for each account so the platform sees one session, and
// a process that dies is restarted a bounded number of times with backoff.
package addon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// ErrUpstreamChanged reports a response the add-on's pin does not produce: a
// shape Core does not know, or an operation the program no longer offers.
var ErrUpstreamChanged = errors.New("upstream changed")

// ErrNotConfigured reports a missing Node.js or program. Only the sources of
// the add-on fail; Core and every other source keep working.
var ErrNotConfigured = errors.New("messenger add-on needs Node.js")

// ErrAttachmentFetch reports that the add-on carries attachment metadata but no
// route to its bytes; fetching one stays with the per-platform issue.
var ErrAttachmentFetch = errors.New("attachment fetch not offered by the messenger add-on")

// ErrStopped reports that the add-on exited and was not restarted.
var ErrStopped = errors.New("messenger add-on stopped")

// Capabilities is the add-on's own descriptor. Each platform driver narrows it.
func Capabilities() messenger.Capabilities {
	return messenger.Capabilities{
		HistoryDepth: "platform-window",
		CursorKind:   "opaque",
		Edits:        true,
		Deletions:    true,
		LiveEvents:   true,
		Interval:     15 * time.Minute,
		ReadWindow:   48 * time.Hour,
	}
}

// Adapter reads one account through the add-on process that serves it.
type Adapter struct {
	platform, account string
	process           *process
}

// Session is the add-on process for one account. Adapter returns a
// messenger.Adapter that never starts a second process for the same account.
type Session struct {
	platform, account string
	process           *process
}

// NewSession starts one add-on process for an account. It returns a session
// even when the process cannot start; the first operation then reports why, so
// the source shows the cause instead of Core failing to boot.
func NewSession(platform, account string, config Config) *Session {
	return &Session{platform: platform, account: account, process: newProcess(platform, account, config)}
}

// Adapter is the messenger adapter boundary for this account.
func (s *Session) Adapter() messenger.Adapter {
	return &Adapter{platform: s.platform, account: s.account, process: s.process}
}

// Close stops the process and joins it.
func (s *Session) Close(ctx context.Context) error { return s.process.Close(ctx) }

// Platform is the platform this session reads.
func (s *Session) Platform() string { return s.platform }

// Account is the account this session reads.
func (s *Session) Account() string { return s.account }

func (a *Adapter) SignIn(ctx context.Context) (messenger.SignInState, error) {
	var result struct {
		State string `json:"state"`
	}
	if err := a.process.call(ctx, "signIn", nil, &result); err != nil {
		return messenger.Failing, err
	}
	switch result.State {
	case "signed_in":
		return messenger.SignedIn, nil
	case "needs_sign_in":
		return messenger.NeedsSignIn, nil
	case "failing":
		return messenger.Failing, nil
	}
	return messenger.Failing, fmt.Errorf("%w: sign-in state %q", ErrUpstreamChanged, result.State)
}

func (a *Adapter) ListChats(ctx context.Context) ([]messenger.Chat, error) {
	var result struct {
		Chats []wireChat `json:"chats"`
	}
	if err := a.process.call(ctx, "listChats", nil, &result); err != nil {
		return nil, err
	}
	chats := make([]messenger.Chat, 0, len(result.Chats))
	for _, value := range result.Chats {
		chat, err := value.chat()
		if err != nil {
			return nil, err
		}
		chats = append(chats, chat)
	}
	return chats, nil
}

func (a *Adapter) Page(ctx context.Context, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	var result wirePage
	if err := a.process.call(ctx, "page", map[string]any{"chat": chat, "cursor": string(cursor)}, &result); err != nil {
		return messenger.Page{}, err
	}
	return result.page()
}

func (a *Adapter) Members(ctx context.Context, chat string) ([]messenger.Author, error) {
	var result struct {
		Members []wireAuthor `json:"members"`
	}
	if err := a.process.call(ctx, "members", map[string]any{"chat": chat}, &result); err != nil {
		return nil, err
	}
	members := make([]messenger.Author, 0, len(result.Members))
	for _, value := range result.Members {
		author, err := value.author()
		if err != nil {
			return nil, err
		}
		members = append(members, author)
	}
	return members, nil
}

// Live delivers the add-on's pushed events until ctx ends.
func (a *Adapter) Live(ctx context.Context, deliver func(messenger.Event) error) error {
	return a.process.live(ctx, deliver)
}

// FetchAttachment is not offered: the operation table carries attachment
// metadata only.
func (a *Adapter) FetchAttachment(context.Context, string, string) (io.ReadCloser, error) {
	return nil, ErrAttachmentFetch
}

func (a *Adapter) Capabilities() messenger.Capabilities { return Capabilities() }

// Manager keeps one add-on session per account for as long as Core runs, so the
// platform sees one session per account and a second sync does not restart it.
type Manager struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*Session
	closed   bool
}

// NewManager composes the add-on's sessions. cfg carries Core's trusted paths.
func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, sessions: map[string]*Session{}}
}

// Addon is the adapter for one account. The manager keeps its session, so a
// later sync reuses the same process.
func (m *Manager) Addon(platform, account string) (messenger.Adapter, error) {
	session, err := m.Start(platform, account)
	if err != nil {
		return nil, err
	}
	return session.Adapter(), nil
}

// Start returns the account's session, starting its process on first use.
func (m *Manager) Start(platform, account string) (*Session, error) {
	if m == nil {
		return nil, ErrNotConfigured
	}
	key := platform + "\x00" + account
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrStopped
	}
	if session, ok := m.sessions[key]; ok {
		return session, nil
	}
	session := NewSession(platform, account, m.cfg)
	m.sessions[key] = session
	return session, nil
}

// Close stops every session and joins each process.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	var err error
	for _, session := range sessions {
		err = errors.Join(err, session.Close(ctx))
	}
	return err
}
