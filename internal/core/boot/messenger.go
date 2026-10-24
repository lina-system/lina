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
// platform boundary. No platform adapter ships yet, so an installed Core starts
// an idle loop until an add-on or a ported adapter provides one.
type MessengerConfig struct {
	// Adapters are keyed by platform.
	Adapters map[string]messenger.Adapter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
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
	r.senderIdentity = identity.Open(r.db, r.person, peopleRecords{store: r.records}, r.removals)
	if _, err := r.senderIdentity.Rematch(ctx); err != nil {
		return err
	}
	store := messenger.Open(r.db, r.materials, r.removals).WithIdentities(r.senderIdentity)
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

// Identity is the sender-identity store the messenger loop matches with.
func (r *conversationRuntime) SenderIdentity() *identity.Store { return r.senderIdentity }
