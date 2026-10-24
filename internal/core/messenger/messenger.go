// SPDX-License-Identifier: Apache-2.0

// Package messenger brings the person's own messenger accounts into canon. A
// source is one account on one platform; its chats each become one messenger
// conversation whose messages are ledger events. The package owns the source
// registry, admission of what an adapter reads, the sync loop and the adapter
// boundary. Nothing here talks to a platform: adapters do. A platform with one
// way in has one adapter. Discord's bot and personal account are two adapters
// of one platform, and every operation names the source account it acts for.
//
// A messenger record is admitted through the trusted entry path: the speaker
// kind comes from the adapter's platform route flag, never from display names
// or text. Records, the chat's cursor and a coverage record commit together, so
// a second read of the same messages adds nothing and an interrupted read
// resumes from the committed cursor.
package messenger

import (
	"context"
	"io"
	"time"
)

// State is a source's registry state.
type State string

const (
	StateConnected    State = "connected"
	StateNeedsSignIn  State = "needs_sign_in"
	StateFailing      State = "failing"
	StateUnreachable  State = "unreachable"
	StateImportOnly   State = "import_only"
	StateDisconnected State = "disconnected"
)

// Mode is whether the adapter uses the platform's documented API.
type Mode string

const (
	ModeOfficial   Mode = "official"
	ModeUnofficial Mode = "unofficial"
)

// Route is the platform's own statement of who wrote a message. An adapter
// derives it from the platform data (the author being the connected account, a
// bot or app author, a platform notice, or anyone else) and never from a
// display name or the message text. LINA's own writes are not a route: Core
// recognises them by matching the message to an effect it recorded.
type Route string

const (
	RoutePerson      Route = "person"
	RouteAutomation  Route = "automation"
	RouteSystem      Route = "system"
	RouteParticipant Route = "external_participant"
)

// Valid reports whether the platform route is one Core can admit.
func (r Route) Valid() bool {
	switch r {
	case RoutePerson, RouteAutomation, RouteSystem, RouteParticipant:
		return true
	}
	return false
}

// Author identifies a message's writer on the platform. It never becomes a LINA
// person or agent id.
type Author struct {
	ID    string
	Name  string
	Email string // Profile evidence; never an identity key.
}

// Attachment is metadata only; its bytes enter LINA when the person asks.
type Attachment struct {
	ID, Name, MIMEType string
	Size               int64
}

// Message is one message as the platform reports it, oldest last in a page. An
// Edited time later than the stored version makes a new record that supersedes
// the earlier one; a zero Edited is the message as first read.
type Message struct {
	ID          string
	Author      Author
	Route       Route
	At          time.Time
	Edited      time.Time
	Text        string
	ReplyTo     string
	Thread      string
	Attachments []Attachment
}

// Deletion is a deletion the platform reports. A message absent from a later
// read is never a deletion.
type Deletion struct {
	Message string
	At      time.Time
}

// Event is one live event the platform pushed: a message or a deletion.
type Event struct {
	Message  *Message
	Deletion *Deletion
}

// Chat is a chat as the platform lists it.
type Chat struct {
	ID             string
	Name           string
	Kind           string // direct, group, channel or thread
	FirstMessageAt time.Time
}

// Cursor is an opaque position inside one chat. It is committed with the
// records it produced, so a resumed read continues after the last committed
// page.
type Cursor string

// Page is one read of a chat from a cursor, oldest first.
type Page struct {
	Messages  []Message
	Deletions []Deletion
	Next      Cursor
	HasMore   bool
	// Through is the newest time this page read completely, when known.
	Through time.Time
}

// Capabilities is the adapter's static descriptor. Only adapters of official
// platforms name write verbs.
type Capabilities struct {
	HistoryDepth string
	CursorKind   string
	Edits        bool
	Deletions    bool
	LiveEvents   bool
	Writes       []string
	// Interval is how often the source should be read, and ReadWindow how far
	// back the platform may still serve a gap.
	Interval   time.Duration
	ReadWindow time.Duration
}

// SignInState is the adapter's current sign-in state.
type SignInState string

const (
	SignedIn    SignInState = "signed_in"
	NeedsSignIn SignInState = "needs_sign_in"
	Failing     SignInState = "failing"
)

// SourceID is the platform account an adapter operation acts for. Two accounts
// of one platform never share a read.
type SourceID struct {
	Platform string
	Account  string
}

// Adapter is the messenger adapter boundary. The service selects it by the
// name the source records, and passes that source's account on every call
// except Capabilities, which describes the adapter rather than one account.
type Adapter interface {
	SignIn(context.Context, SourceID) (SignInState, error)
	ListChats(context.Context, SourceID) ([]Chat, error)
	// Page reads one page of a chat from a cursor, oldest first.
	Page(context.Context, SourceID, string, Cursor) (Page, error)
	Members(context.Context, SourceID, string) ([]Author, error)
	// Live delivers events the platform pushes, until ctx ends.
	Live(context.Context, SourceID, func(Event) error) error
	// FetchAttachment reads one attachment on request.
	FetchAttachment(context.Context, SourceID, string, string) (io.ReadCloser, error)
	Capabilities() Capabilities
}

// Terms is the record of the person accepting an unofficial platform's terms
// before connecting.
type Terms struct {
	Person string
	At     time.Time
	// Text names the term the person accepted, in the platform's words.
	Text string
}

// Source is one account on one platform.
type Source struct {
	Person            string
	Platform          string
	Account           string
	Mode              Mode
	Adapter           string
	State             State
	Failure           string
	LearningWatermark string
	// Learn is the person's source-wide learning switch. A chat's own switch
	// narrows it further.
	Learn         bool
	AcceptedTerms *Terms
	ConnectedAt   time.Time
}

// Holder is what holds a messenger record that has no participating agent. The
// ledger needs a non-empty holder; this id names the source and never matches
// an agent, so no agent reads a messenger conversation as its own.
func (s Source) Holder() string { return "messenger:" + s.Platform + ":" + s.Account }

// ChatRecord is one chat of a source.
type ChatRecord struct {
	Platform, Account, Chat string
	Name, Kind              string
	FirstMessageAt          time.Time
	Sync, Learn, Embed      bool
	// Scope is the chat's sensitivity class. A never-leaves chat is searchable
	// but never learning input.
	Scope  string
	Cursor Cursor
}

// Chat scopes. They match the memory scope names; the messenger package keeps
// its own strings so it does not import the memory store.
const (
	ChatLocal       = "local"
	ChatPrivate     = "private"
	ChatNeverLeaves = "never-leaves"
)

// Switches are the person's choices for one chat.
type Switches struct {
	Sync, Learn, Embed bool
}

// Freshness is a chat's derived coverage state.
type Freshness struct {
	State    string // fresh, stale or gap
	LastRead time.Time
	Age      time.Duration
}

// Effect is a write LINA made in the person's name. Core records it, with the
// platform's client message id when the platform takes one, before it admits
// that chat's own-account messages; the message that comes back through sync is
// matched to this effect and admitted as an agent record.
type Effect struct {
	ID, Platform, Account, Chat, Agent, Verb string
	ClientID                                 string
	Message                                  string
	State                                    string // pending, sent, unknown, failed
	CreatedAt, SettledAt                     time.Time
}

// Effect states.
const (
	EffectPending = "pending"
	EffectSent    = "sent"
	EffectUnknown = "unknown"
	EffectFailed  = "failed"
)

// Result reports one sync.
type Result struct {
	Chats    int
	Added    int
	Updated  int
	Deleted  int
	Waiting  int
	Coverage int
}

func (r *Result) add(o Result) {
	r.Chats += o.Chats
	r.Added += o.Added
	r.Updated += o.Updated
	r.Deleted += o.Deleted
	r.Waiting += o.Waiting
	r.Coverage += o.Coverage
}
