// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	gatewaydiscord "github.com/thisisjun786/lina/internal/core/gateway/discord"
	"github.com/thisisjun786/lina/internal/core/messenger"
	messengerdiscord "github.com/thisisjun786/lina/internal/core/messenger/discord"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// goneClient tells the source adapter when a message can never be read. The
// platform's not-found and forbidden answers are permanent; every other read
// failure is transient and the live update is replayed.
type goneClient struct{ *gatewaydiscord.Client }

func (c goneClient) Get(ctx context.Context, path string, query url.Values, out any) error {
	err := c.Client.Get(ctx, path, query, out)
	if errors.Is(err, gatewaydiscord.ErrNotFound) || errors.Is(err, gatewaydiscord.ErrForbidden) {
		return messengerdiscord.ErrGone
	}
	return err
}

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary. An explicitly configured adapter, such as a test's, takes
// the recorded adapter's place; a Discord bot source is resolved from the token
// the person stored for that account.
type MessengerConfig struct {
	// Adapters are keyed by the name recorded on each source. Every read also
	// receives its SourceID. Addon resolves a separate session per account.
	Adapters map[string]messenger.Adapter
	// Addon starts one add-on session per account of an unofficial platform. A
	// nil Addon leaves those sources without an adapter, so Core starts an idle
	// loop.
	Addon AddonStarter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
	// Transport is an in-process platform seam for tests.
	Transport http.RoundTripper
}

// peopleRecords adapts the records repository to sender identity: the record
// title and aliases are the names a person keeps for someone, and the contact
// section holds the identifiers that may link on their own.
type peopleRecords struct{ store *records.Store }

func (p peopleRecords) People(ctx context.Context, person string) ([]identity.Person, error) {
	recordsList, err := p.store.People(ctx, person)
	if err != nil {
		return nil, err
	}
	people := make([]identity.Person, 0, len(recordsList))
	for _, record := range recordsList {
		known := identity.Person{Record: record.ID}
		if record.Title != "" {
			known.Names = append(known.Names, record.Title)
		}
		for _, alias := range record.Names {
			known.Names = append(known.Names, alias)
		}
		for _, contact := range record.Contacts {
			known.Identifiers = append(known.Identifiers, identity.SeenIdentifier{Kind: identity.Kind(contact.Kind), Value: contact.Value, Source: contact.Source})
		}
		people = append(people, known)
	}
	return people, nil
}

// AddonStarter supplies the add-on adapter for one account and closes every
// session when Core stops.
type AddonStarter interface {
	Addon(platform, account string) (messenger.Adapter, error)
	Close(context.Context) error
}

// senderIdentities returns the identity store this conversation admits through,
// composing it on first use and matching the identities already recorded. Every
// messenger reader shares it, so an author read by the sync loop, the Teams
// service or the Webex service is matched the same way.
func (r *conversationRuntime) senderIdentities(ctx context.Context) (*identity.Store, error) {
	if r.senderIdentity != nil {
		return r.senderIdentity, nil
	}
	store := identity.Open(r.db, r.person, peopleRecords{store: r.records}, r.removals)
	if _, err := store.Rematch(ctx); err != nil {
		return nil, err
	}
	r.senderIdentity = store
	return store, nil
}

// messengerStore opens the registry and admission store on this conversation's
// canon with sender identity attached.
func (r *conversationRuntime) messengerStore(ctx context.Context) (*messenger.Store, error) {
	identities, err := r.senderIdentities(ctx)
	if err != nil {
		return nil, err
	}
	return messenger.Open(r.db, r.materials, r.removals).WithIdentities(identities), nil
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops. Sender identity
// is composed here so every admitted author is matched to a people record.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	store, err := r.messengerStore(ctx)
	if err != nil {
		return err
	}
	service := messenger.NewServiceWithResolver(store, r.messengerResolver(cfg), cfg.Interval)
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerAddon, r.messengerStop, r.messengerDone = service, cfg.Addon, stop, make(chan struct{})
	go func() {
		service.Run(runCtx)
		// Close before signalling, so shutdown waits for the adapters' idle
		// connections to be released.
		service.Close()
		close(r.messengerDone)
	}()
	return nil
}

// messengerResolver composes the registered adapters and the add-on sessions
// with the bot sources this Core reads from the person's stored token.
func (r *conversationRuntime) messengerResolver(cfg *MessengerConfig) messenger.Resolver {
	registered := resolveMessenger(cfg)
	return func(source messenger.Source) (messenger.Adapter, bool) {
		if adapter, ok := registered(source); ok {
			return adapter, true
		}
		adapter, err := r.botSource(source, cfg.Transport)
		return adapter, err == nil
	}
}

// botSource reads a Discord bot source with the token the person stored for
// that account. Each source gets its own adapter, so two accounts on one
// platform never share read state, and a shared token still runs one Gateway
// session.
func (r *conversationRuntime) botSource(source messenger.Source, transport http.RoundTripper) (messenger.Adapter, error) {
	if source.Platform != "discord" || source.Mode != messenger.ModeOfficial {
		return nil, messenger.ErrInvalid
	}
	token, err := r.vault.Get(secrets.Key{Person: source.Person, Service: "messenger:discord", Name: source.Account})
	if errors.Is(err, secrets.ErrNotFound) {
		// The token may be the one a channel connection stored for the same bot.
		token, err = r.vault.Get(secrets.Key{Person: source.Person, Service: "discord", Name: source.Account})
	}
	if err != nil {
		return nil, messenger.ErrNotConnected
	}
	client, err := gatewaydiscord.NewClient(token, transport)
	if err != nil {
		return nil, err
	}
	adapter, err := messengerdiscord.New(messengerdiscord.Config{
		Source: source, DB: r.db, Client: goneClient{client},
		Listen: func(ctx context.Context, handle func(context.Context, string, json.RawMessage) error) error {
			err := gatewaydiscord.Listen(ctx, gatewaydiscord.Listener{
				Bot:   channels.Bot{Platform: source.Platform, Account: source.Account},
				Token: token, DB: r.db, Transport: transport,
				Intents: gatewaydiscord.IntentMessageContent,
				Handle: func(ctx context.Context, kind string, data json.RawMessage, _ string) error {
					return handle(ctx, kind, data)
				},
			})
			if errors.Is(err, gatewaydiscord.ErrIntentsDenied) {
				return messenger.ErrTextUnavailable
			}
			return err
		},
	})
	if err != nil {
		client.Close()
	}
	return adapter, err
}

// Messenger is the running source loop, when Core started one.
func (r *conversationRuntime) Messenger() *messenger.Service { return r.messenger }

// Identity is the sender-identity store the messenger loop matches with.
func (r *conversationRuntime) SenderIdentity() *identity.Store { return r.senderIdentity }

// resolveMessenger selects an adapter per source: a platform adapter when one
// is registered, otherwise the add-on session for that account. The starter
// keeps one session per account, so the platform sees one session.
func resolveMessenger(cfg *MessengerConfig) messenger.Resolver {
	return func(source messenger.Source) (messenger.Adapter, bool) {
		if adapter, ok := cfg.Adapters[source.Adapter]; ok {
			return adapter, true
		}
		if cfg.Addon == nil || source.Mode != messenger.ModeUnofficial || source.Adapter != "messenger-addon" {
			return nil, false
		}
		adapter, err := cfg.Addon.Addon(source.Platform, source.Account)
		if err != nil || adapter == nil {
			return nil, false
		}
		return adapter, true
	}
}
