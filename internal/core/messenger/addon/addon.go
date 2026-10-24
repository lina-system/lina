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
	"os"
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

// ErrLiveEnded reports a live subscription the add-on ended on its own, such
// as after a lost connection. The add-on keeps serving; the source is read
// again on its schedule.
var ErrLiveEnded = errors.New("messenger add-on live events ended")

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
	platform, key string
	single        bool
	process       *process
	// describe reads the platform driver's descriptor once the session has it;
	// nil means the add-on's own descriptor still applies.
	describe func() (messenger.Capabilities, bool)
	session  *Session
}

// Session is the add-on process for one account. Adapter returns a
// messenger.Adapter that never starts a second process for the same account.
type Session struct {
	platform, key string
	single        bool
	process       *process

	capsMu sync.Mutex
	// caps is the platform driver's descriptor, read once per session.
	caps *messenger.Capabilities
}

// NewSession starts one add-on process for an account. It returns a session
// even when the process cannot start; the first operation then reports why, so
// the source shows the cause instead of Core failing to boot.
func NewSession(platform, account string, config Config) *Session {
	return newSession(platform, account, false, config)
}

// newSession composes one process. A single-account session answers for every
// account of its platform, because LINA runs one session for that platform.
func newSession(platform, key string, single bool, config Config) *Session {
	return &Session{platform: platform, key: key, single: single, process: newProcess(platform, key, config)}
}

// Adapter is the messenger adapter boundary for this account.
func (s *Session) Adapter() messenger.Adapter {
	return &Adapter{platform: s.platform, key: s.key, single: s.single, process: s.process, describe: s.descriptor, session: s}
}

// descriptor returns the driver's descriptor if this session already read it.
func (s *Session) descriptor() (messenger.Capabilities, bool) {
	s.capsMu.Lock()
	defer s.capsMu.Unlock()
	if s.caps == nil {
		return messenger.Capabilities{}, false
	}
	return *s.caps, true
}

// keepDescriptor records the platform's descriptor from a signIn reply.
func (s *Session) keepDescriptor(raw *wireCapabilities) error {
	if raw == nil {
		return nil
	}
	caps, err := raw.capabilities()
	if err != nil {
		return err
	}
	s.capsMu.Lock()
	s.caps = &caps
	s.capsMu.Unlock()
	return nil
}

// Close stops the process and joins it.
func (s *Session) Close(ctx context.Context) error { return s.process.Close(ctx) }

// Platform is the platform this session reads.
func (s *Session) Platform() string { return s.platform }

// Account is the session's own identity: the account, or the platform where
// LINA keeps one session for it.
func (s *Session) Account() string { return s.key }

func (a *Adapter) SignIn(ctx context.Context, id messenger.SourceID) (messenger.SignInState, error) {
	if err := a.forAccount(id); err != nil {
		return messenger.Failing, err
	}
	var result struct {
		State        string            `json:"state"`
		Capabilities *wireCapabilities `json:"capabilities"`
	}
	if err := a.process.call(ctx, "signIn", nil, &result); err != nil {
		return messenger.Failing, err
	}
	if err := a.session.keepDescriptor(result.Capabilities); err != nil {
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

func (a *Adapter) ListChats(ctx context.Context, id messenger.SourceID) ([]messenger.Chat, error) {
	if err := a.forAccount(id); err != nil {
		return nil, err
	}
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

func (a *Adapter) Page(ctx context.Context, id messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if err := a.forAccount(id); err != nil {
		return messenger.Page{}, err
	}
	var result wirePage
	if err := a.process.call(ctx, "page", map[string]any{"chat": chat, "cursor": string(cursor)}, &result); err != nil {
		return messenger.Page{}, err
	}
	return result.page()
}

func (a *Adapter) Members(ctx context.Context, id messenger.SourceID, chat string) ([]messenger.Author, error) {
	if err := a.forAccount(id); err != nil {
		return nil, err
	}
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

// Live delivers the add-on's pushed events until ctx ends. A subscription the
// add-on ends on its own returns ErrLiveEnded with its cause, joined with
// messenger.ErrNeedsSignIn when the platform revoked the session.
func (a *Adapter) Live(ctx context.Context, id messenger.SourceID, deliver func(messenger.Event) error) error {
	if err := a.forAccount(id); err != nil {
		return err
	}
	return a.process.live(ctx, deliver)
}

// FetchAttachment is not offered: the operation table carries attachment
// metadata only.
func (a *Adapter) FetchAttachment(_ context.Context, id messenger.SourceID, _, _ string) (io.ReadCloser, error) {
	if err := a.forAccount(id); err != nil {
		return nil, err
	}
	return nil, ErrAttachmentFetch
}

func (a *Adapter) Capabilities() messenger.Capabilities {
	if a.describe != nil {
		if caps, ok := a.describe(); ok {
			return caps
		}
	}
	return Capabilities()
}

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

// Start returns the account's session, starting its process on first use. A
// platform listed in SingleAccount has one session for every account, so the
// session outlives a source's account changing from the pending sign-in to the
// platform's own user id.
func (m *Manager) Start(platform, account string) (*Session, error) {
	if m == nil {
		return nil, ErrNotConfigured
	}
	key := m.key(platform, account)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrStopped
	}
	if session, ok := m.sessions[key]; ok {
		return session, nil
	}
	session := newSession(platform, key, key == platform, m.cfg)
	m.sessions[key] = session
	return session, nil
}

// Stop closes one account's session and forgets it, so a later start begins a
// new process. Disconnecting a source uses it to end the platform session.
func (m *Manager) Stop(ctx context.Context, platform, account string) error {
	if m == nil {
		return nil
	}
	key := m.key(platform, account)
	m.mu.Lock()
	session, ok := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return session.Close(ctx)
}

// Remove closes one account's session and deletes its data directory, so a
// disconnected source loses the platform session it held.
func (m *Manager) Remove(ctx context.Context, platform, account string) error {
	if m == nil {
		return nil
	}
	key := m.key(platform, account)
	if err := m.Stop(ctx, platform, account); err != nil {
		return err
	}
	dir := DataDir(m.cfg.StateRoot, platform, key)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

// key is the session identity: the platform itself where LINA supports one
// account, the account otherwise.
func (m *Manager) key(platform, account string) string {
	for _, single := range m.cfg.SingleAccount {
		if single == platform {
			return platform
		}
	}
	return account
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

// forAccount enforces the same source boundary as the official adapters. A
// single-account platform has one session for every account it serves.
func (a *Adapter) forAccount(id messenger.SourceID) error {
	if id.Platform != a.platform || id.Account == "" {
		return messenger.ErrInvalid
	}
	if !a.single && id.Account != a.key {
		return messenger.ErrInvalid
	}
	return nil
}
