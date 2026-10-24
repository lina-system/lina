// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/discorduser"
)

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary.
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
	// ChatsReadElsewhere reports chats another source of the same platform
	// already reads through the platform's own API. Core sets it from the
	// registry, so a personal account leaves those chats to the bot source.
	ChatsReadElsewhere func(ctx context.Context, person, platform, except string) (map[string]bool, error)
}

// AddonStarter supplies the add-on adapter for one account and closes every
// session when Core stops.
type AddonStarter interface {
	Addon(platform, account string) (messenger.Adapter, error)
	Close(context.Context) error
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	if cfg.Addon == nil && len(cfg.Adapters) == 0 {
		return nil
	}
	store := messenger.Open(r.db, r.materials, r.removals)
	settings := *cfg
	settings.ChatsReadElsewhere = store.ChatsReadElsewhere
	service := messenger.NewServiceWithResolver(store, resolveMessenger(&settings), settings.Interval)
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerAddon, r.messengerStop, r.messengerDone = service, cfg.Addon, stop, make(chan struct{})
	go func() {
		defer close(r.messengerDone)
		service.Run(runCtx)
	}()
	return nil
}

// Messenger is the running source loop, when Core started one.
func (r *conversationRuntime) Messenger() *messenger.Service { return r.messenger }

// resolveMessenger selects an adapter per source: a platform adapter when one
// is registered, otherwise the add-on session for that account. The starter
// keeps one session per account, so the platform sees one session.
func resolveMessenger(cfg *MessengerConfig) messenger.Resolver {
	return func(source messenger.Source) (messenger.Adapter, bool) {
		if adapter, ok := cfg.Adapters[source.Adapter]; ok {
			return adapter, true
		}
		if cfg.Addon == nil || source.Mode != messenger.ModeUnofficial || source.Adapter != messenger.AddonAdapter {
			return nil, false
		}
		adapter, err := cfg.Addon.Addon(source.Platform, source.Account)
		if err != nil || adapter == nil {
			return nil, false
		}
		if source.Platform == discorduser.Platform && cfg.ChatsReadElsewhere != nil {
			// The same chat through the bot and through the person's account
			// would be read twice; the personal account leaves it to the bot.
			person, platform, account := source.Person, source.Platform, source.Account
			return discorduser.Once(adapter, func(ctx context.Context) (map[string]bool, error) {
				return cfg.ChatsReadElsewhere(ctx, person, platform, account)
			}), true
		}
		return adapter, true
	}
}
