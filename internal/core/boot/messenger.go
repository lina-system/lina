// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/records"
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
}

// AddonStarter supplies the add-on adapter for one account and closes every
// session when Core stops.
type AddonStarter interface {
	Addon(platform, account string) (messenger.Adapter, error)
	Close(context.Context) error
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

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops. Sender identity
// is composed here so every admitted author is matched to a people record.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	if cfg.Addon == nil && len(cfg.Adapters) == 0 {
		return nil
	}
	r.senderIdentity = identity.Open(r.db, r.person, peopleRecords{store: r.records}, r.removals)
	if _, err := r.senderIdentity.Rematch(ctx); err != nil {
		return err
	}
	store := messenger.Open(r.db, r.materials, r.removals).WithIdentities(r.senderIdentity)
	service := messenger.NewServiceWithResolver(store, resolveMessenger(cfg), cfg.Interval)
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

// SenderIdentity is the sender-identity store the messenger loop matches with.
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
