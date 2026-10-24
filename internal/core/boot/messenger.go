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
	// Adapters are keyed by the adapter name a source records. A platform with
	// one adapter uses the platform name. Discord's bot and personal account
	// register two names, and each call names the source account.
	Adapters map[string]messenger.Adapter
	// Factory opens a per-source adapter before Core's own. A test points it at
	// an in-process platform; production leaves it nil.
	Factory messenger.Factory
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule and from their live events until
// Core stops.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	store := messenger.Open(r.db, r.materials, r.removals)
	service := messenger.NewService(store, cfg.Adapters, cfg.Interval).WithFactory(r.messengerFactory(cfg.Factory))
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerStop, r.messengerDone = service, stop, make(chan struct{})
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
		if source.Platform != telegram.Platform {
			return nil, false, nil
		}
		adapter, err := telegram.New(telegram.Config{Person: source.Person, Account: source.Account, Vault: vault})
		return adapter, err == nil, err
	}
}

// Messenger is the running source loop, when Core started one.
func (r *conversationRuntime) Messenger() *messenger.Service { return r.messenger }
