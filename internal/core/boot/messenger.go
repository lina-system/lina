// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary. Teams runs account-specific workers through the installed
// Teams service; this seam also supports independently supplied adapters.
type MessengerConfig struct {
	// Adapters are keyed by platform.
	Adapters map[string]messenger.Adapter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	store := messenger.Open(r.db, r.materials, r.removals)
	service := messenger.NewService(store, cfg.Adapters, cfg.Interval)
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerStop, r.messengerDone = service, stop, make(chan struct{})
	go func() {
		defer close(r.messengerDone)
		service.Run(runCtx)
	}()
	return nil
}

// Messenger is the running source loop, when Core started one.
func (r *conversationRuntime) Messenger() *messenger.Service { return r.messenger }
