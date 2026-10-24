// SPDX-License-Identifier: Apache-2.0

// Package discord reads the Discord servers where the person added a bot, as a
// messenger source. It uses LINA's own Discord adapter for the Gateway and
// REST: one session per bot token, shared with a channel connection on the same
// token, and the same fixed API origin.
//
// The source never guesses an author from a display name or from message text.
// The person is the numeric user id the channel pairing confirmed, a bot author
// is automation, a platform notice is system, and everyone else is an external
// participant.
package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

// Client is the read-only Discord REST port supplied by the composition root.
type Client interface {
	Get(context.Context, string, url.Values, any) error
	VerifyBot(context.Context, string) (bool, error)
	Close()
}

// Listen joins the shared Gateway and reports unavailable text as
// messenger.ErrTextUnavailable.
type Listen func(context.Context, func(context.Context, string, json.RawMessage) error) error

// Config binds one bot-mode Discord source.
type Config struct {
	Source messenger.Source
	// DB supplies the pairing that confirms the person's own numeric user id.
	DB     channels.Canon
	Client Client
	Listen Listen
}

// Adapter reads one Discord bot's servers as a messenger source.
type Adapter struct {
	source messenger.Source
	bot    channels.Bot
	db     channels.Canon
	client Client
	listen Listen

	mu     sync.Mutex
	chats  map[string]chatInfo
	loaded bool
}

// ErrUnsupported reports that the source cannot serve this operation.
var ErrUnsupported = errors.New("discord_source_unsupported")

var snowflake = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// New composes the source adapter. Nothing here contacts Discord.
func New(c Config) (*Adapter, error) {
	if c.Source.Platform != "discord" || !snowflake.MatchString(c.Source.Account) || c.Source.Mode != messenger.ModeOfficial || c.DB == nil || c.Client == nil || c.Listen == nil {
		return nil, messenger.ErrInvalid
	}
	return &Adapter{
		source: c.Source,
		bot:    channels.Bot{Platform: "discord", Account: c.Source.Account},
		db:     c.DB,
		client: c.Client,
		listen: c.Listen,
		chats:  map[string]chatInfo{},
	}, nil
}

// Close releases idle connections.
func (a *Adapter) Close() { a.client.Close() }

// Capabilities describes Discord's bot-mode reads.
func (a *Adapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{
		HistoryDepth: "full",
		CursorKind:   "snowflake",
		Edits:        true,
		Deletions:    true,
		LiveEvents:   true,
		Interval:     15 * time.Minute,
	}
}

// SignIn checks the token against Discord's own answer and the source's
// account. A refused token needs the person.
func (a *Adapter) SignIn(ctx context.Context, id messenger.SourceID) (messenger.SignInState, error) {
	if id != a.sourceID() {
		return messenger.Failing, messenger.ErrInvalid
	}
	valid, err := a.client.VerifyBot(ctx, a.source.Account)
	if err != nil {
		return messenger.Failing, err
	}
	if !valid {
		return messenger.NeedsSignIn, nil
	}
	return messenger.SignedIn, nil
}

// personID is the numeric user id that proves a message is the person's: the
// one the source recorded at connect, or, for a source sharing the token with a
// channel connection, the pairing's sender. Without either, nobody is claimed.
func (a *Adapter) personID(ctx context.Context) string {
	if snowflake.MatchString(a.source.Owner) {
		return a.source.Owner
	}
	var sender string
	err := a.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT sender FROM channel_connections WHERE bot=?", a.bot.Key()).Scan(&sender)
	})
	if err != nil || !snowflake.MatchString(sender) {
		return ""
	}
	return sender
}

// FetchAttachment needs the platform's signed CDN location, which this
// increment does not store. Attachment metadata still reaches the record.
func (a *Adapter) FetchAttachment(_ context.Context, id messenger.SourceID, _, _ string) (io.ReadCloser, error) {
	if id != a.sourceID() {
		return nil, messenger.ErrInvalid
	}
	return nil, ErrUnsupported
}

func (a *Adapter) sourceID() messenger.SourceID {
	return messenger.SourceID{Platform: a.source.Platform, Account: a.source.Account}
}
