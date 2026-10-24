// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	gatewaydiscord "github.com/thisisjun786/lina/internal/core/gateway/discord"
	"github.com/thisisjun786/lina/internal/core/messenger"
	messengerdiscord "github.com/thisisjun786/lina/internal/core/messenger/discord"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary. A platform ported here is resolved from its source account
// and that account's stored token; an explicitly configured adapter, such as a
// test's, takes its platform's place.
// Teams runs account-specific workers through the installed Teams service.
type MessengerConfig struct {
	// Adapters are keyed by platform and replace Core's own adapter there.
	Adapters map[string]messenger.Adapter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
	// Transport is an in-process platform seam for tests.
	Transport http.RoundTripper
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	store := messenger.Open(r.db, r.materials, r.removals)
	service := messenger.NewServiceWith(store, r.messengerAdapter(cfg), cfg.Interval)
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerStop, r.messengerDone = service, stop, make(chan struct{})
	go func() {
		service.Run(runCtx)
		// Close before signalling, so shutdown waits for the adapters' idle
		// connections to be released.
		service.Close()
		close(r.messengerDone)
	}()
	return nil
}

// messengerAdapter resolves one source's adapter. Discord bot sources read
// with the bot token the person stored for that account, and each source gets
// its own adapter so two accounts on one platform never share read state.
func (r *conversationRuntime) messengerAdapter(cfg *MessengerConfig) func(messenger.Source) (messenger.Adapter, error) {
	return func(source messenger.Source) (messenger.Adapter, error) {
		if adapter, ok := cfg.Adapters[source.Platform]; ok {
			return adapter, nil
		}
		if source.Platform != "discord" || source.Mode != messenger.ModeOfficial {
			return nil, messenger.ErrInvalid
		}
		token, err := r.vault.Get(secrets.Key{Person: source.Person, Service: "messenger:discord", Name: source.Account})
		if errors.Is(err, secrets.ErrNotFound) {
			// The token may be the one a channel connection stored for the same
			// bot; a shared token still runs one Gateway session.
			token, err = r.vault.Get(secrets.Key{Person: source.Person, Service: "discord", Name: source.Account})
		}
		if err != nil {
			return nil, messenger.ErrNotConnected
		}
		client, err := gatewaydiscord.NewClient(token, cfg.Transport)
		if err != nil {
			return nil, err
		}
		adapter, err := messengerdiscord.New(messengerdiscord.Config{
			Source: source, DB: r.db, Client: client,
			Listen: func(ctx context.Context, handle func(context.Context, string, json.RawMessage) error) error {
				err := gatewaydiscord.Listen(ctx, gatewaydiscord.Listener{
					Bot:   channels.Bot{Platform: source.Platform, Account: source.Account},
					Token: token, DB: r.db, Transport: cfg.Transport,
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
}

// Messenger is the running source loop, when Core started one.
func (r *conversationRuntime) Messenger() *messenger.Service { return r.messenger }
