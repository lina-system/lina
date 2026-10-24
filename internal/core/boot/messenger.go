// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
)

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary. Account-specific adapters are opened through Factory.
type MessengerConfig struct {
	// Adapters are keyed by the name recorded on each source. Every read also
	// receives its SourceID. Addon resolves a separate session per account.
	Adapters map[string]messenger.Adapter
	// Factory opens a per-source adapter before Core's own. A test points it at
	// an in-process platform; production leaves it nil.
	Factory messenger.Factory
	// Addon starts one add-on session per account of an unofficial platform. A
	// nil Addon leaves those sources without an adapter, so Core starts an idle
	// loop.
	Addon AddonStarter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
}

// AddonStarter supplies the add-on adapter for one account and closes every
// session when Core stops.
type AddonStarter interface {
	Addon(platform, account string) (messenger.Adapter, error)
	Close(context.Context) error
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule and from their live events until
// Core stops.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if r.messenger != nil {
		return nil
	}
	if cfg == nil {
		cfg = &MessengerConfig{}
	}
	if r.telegram != nil {
		r.telegram.factory = r.messengerFactory(cfg.Factory)
	}
	store := messenger.Open(r.db, r.materials, r.removals)
	service := messenger.NewServiceWithResolver(store, resolveMessenger(cfg), cfg.Interval).WithFactory(r.messengerFactory(cfg.Factory))
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerAddon, r.messengerStop, r.messengerDone = service, cfg.Addon, stop, make(chan struct{})
	go func() {
		defer close(r.messengerDone)
		service.Run(runCtx)
	}()
	return nil
}

// messengerFactory opens a source's adapter. The shipped Telegram adapter is
// available for every installed Core; an override serves a platform whose
// transport a test replaces.
func (r *conversationRuntime) messengerFactory(override messenger.Factory) messenger.Factory {
	vault := r.vault
	return func(source messenger.Source) (messenger.Adapter, bool, error) {
		if override != nil {
			adapter, ok, err := override(source)
			if ok || err != nil {
				return adapter, ok, err
			}
		}
		if source.Platform != telegram.Platform || source.Adapter != telegram.Platform {
			return nil, false, nil
		}
		adapter, err := telegram.New(telegram.Config{Person: source.Person, Account: source.Account, Vault: vault})
		return adapter, err == nil, err
	}
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
