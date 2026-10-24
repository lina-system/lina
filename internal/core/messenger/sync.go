// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"errors"
	"sync"
	"time"
)

// DefaultInterval is the schedule for a source whose adapter states none.
const DefaultInterval = time.Minute

// Service reads connected sources on their schedule and from their live events.
// One source's failure never blocks another source or the conversation.
type Service struct {
	store    *Store
	adapters map[string]Adapter
	interval time.Duration

	mu       sync.Mutex
	lastSync map[string]time.Time
}

// NewService composes the sync loop. adapters are keyed by the adapter name a
// source records. A platform with one adapter uses the platform name; Discord's
// bot and personal account register two names. A source whose name is absent
// is left untouched.
func NewService(store *Store, adapters map[string]Adapter, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Service{store: store, adapters: adapters, interval: interval, lastSync: map[string]time.Time{}}
}

// Store is the registry and admission store the service reads into.
func (s *Service) Store() *Store { return s.store }

// Run reads every connected source until ctx ends. It never returns an error:
// a failing source is recorded in its own registry row.
func (s *Service) Run(ctx context.Context) {
	s.SyncAll(ctx, nil)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncAll(ctx, nil)
		}
	}
}

// SyncAll reads each connected source that has an adapter. now overrides the
// clock for deterministic tests.
func (s *Service) SyncAll(ctx context.Context, now *time.Time) map[string]Result {
	results := map[string]Result{}
	current := time.Now()
	if now != nil {
		current = *now
	}
	sources, err := s.store.allSources(ctx)
	if err != nil {
		return results
	}
	for _, source := range sources {
		if source.State != StateConnected {
			continue
		}
		key := source.Platform + "\x00" + source.Account
		s.mu.Lock()
		last := s.lastSync[key]
		s.mu.Unlock()
		if interval := s.intervalOf(source); !last.IsZero() && current.Sub(last) < interval {
			continue
		}
		result, err := s.Sync(ctx, source)
		s.mu.Lock()
		s.lastSync[key] = current
		s.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			_ = s.store.MarkFailure(ctx, source.Person, source.Platform, source.Account, failureCause(err))
		} else if err == nil {
			results[key] = result
		}
	}
	return results
}

func (s *Service) intervalOf(source Source) time.Duration {
	if adapter, ok := s.adapterFor(source); ok {
		if interval := adapter.Capabilities().Interval; interval > 0 {
			return interval
		}
	}
	return s.interval
}

// adapterFor selects the implementation the source recorded. Lookup is not by
// platform: two accounts on one platform, or a bot and a personal account, would
// otherwise share one instance and one read.
func (s *Service) adapterFor(source Source) (Adapter, bool) {
	adapter, ok := s.adapters[source.Adapter]
	return adapter, ok
}

func sourceID(source Source) SourceID {
	return SourceID{Platform: source.Platform, Account: source.Account}
}

// Sync reads one source: its chat list, then every chat that is in sync.
func (s *Service) Sync(ctx context.Context, source Source) (Result, error) {
	adapter, ok := s.adapterFor(source)
	if !ok {
		return Result{}, ErrInvalid
	}
	id := sourceID(source)
	state, err := adapter.SignIn(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if state == NeedsSignIn {
		if err := s.store.setState(ctx, source.Person, source.Platform, source.Account, StateNeedsSignIn, ""); err != nil {
			return Result{}, err
		}
		return Result{}, nil
	}
	chats, err := adapter.ListChats(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if err := s.store.UpsertChats(ctx, source, chats); err != nil {
		return Result{}, err
	}
	records, err := s.store.Chats(ctx, source.Person, source.Platform, source.Account)
	if err != nil {
		return Result{}, err
	}
	var result Result
	for _, chat := range records {
		if !chat.Sync {
			continue
		}
		outcome, err := s.syncChat(ctx, source, adapter, chat)
		result.add(outcome)
		if err != nil {
			return result, err
		}
	}
	return result, s.store.MarkConnected(ctx, source.Person, source.Platform, source.Account)
}

// syncChat reads pages oldest first from the committed cursor until the platform
// has no more. Each page commits its records, cursor and coverage together, so
// an interrupted read resumes after the last committed page.
func (s *Service) syncChat(ctx context.Context, source Source, adapter Adapter, chat ChatRecord) (Result, error) {
	cursor := chat.Cursor
	var result Result
	result.Chats = 1
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := adapter.Page(ctx, sourceID(source), chat.Chat, cursor)
		if err != nil {
			return result, err
		}
		outcome, err := s.store.AdmitPage(ctx, source, chat.Chat, cursor, page)
		result.Added += outcome.Added
		result.Updated += outcome.Updated
		result.Deleted += outcome.Deleted
		if errors.Is(err, ErrPageWaiting) {
			result.Waiting += outcome.Waiting
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if !page.HasMore {
			result.Coverage++
			return result, nil
		}
		if page.Next == "" || page.Next == cursor {
			return result, ErrInvalid
		}
		cursor = page.Next
	}
}

// Live reads a source's pushed events until ctx ends. A platform without live
// events returns immediately.
func (s *Service) Live(ctx context.Context, source Source, deliver func(Event) error) error {
	adapter, ok := s.adapterFor(source)
	if !ok {
		return ErrInvalid
	}
	return adapter.Live(ctx, sourceID(source), deliver)
}

// failureCause keeps a short, bounded cause on the registry row.
func failureCause(err error) string {
	text := err.Error()
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}
