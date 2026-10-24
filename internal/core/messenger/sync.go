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
	factory  Factory
	interval time.Duration

	mu        sync.Mutex
	lastSync  map[string]time.Time
	perSource map[string]Adapter
}

// Factory opens the adapter of one source. Core uses it for a platform whose
// adapter needs the source's own credentials, such as Telegram. It returns
// false when it does not serve that source.
type Factory func(Source) (Adapter, bool, error)

// NewService composes the sync loop. adapters are keyed by platform and serve
// every source of that platform; factory opens an adapter for a source that
// needs its own. A source that neither serves is left untouched.
func NewService(store *Store, adapters map[string]Adapter, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Service{store: store, adapters: adapters, interval: interval, lastSync: map[string]time.Time{}, perSource: map[string]Adapter{}}
}

// WithFactory adds the per-source adapter factory.
func (s *Service) WithFactory(factory Factory) *Service {
	s.factory = factory
	return s
}

// Close releases per-source adapters that hold a connection.
func (s *Service) Close() {
	s.mu.Lock()
	adapters := make([]Adapter, 0, len(s.perSource))
	for key, adapter := range s.perSource {
		adapters = append(adapters, adapter)
		delete(s.perSource, key)
	}
	s.mu.Unlock()
	for _, adapter := range adapters {
		if closer, ok := adapter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

// adapterFor resolves the adapter of one source.
func (s *Service) adapterFor(source Source) (Adapter, bool) {
	if adapter, ok := s.adapters[source.Platform]; ok {
		return adapter, true
	}
	key := source.Platform + "\x00" + source.Account
	// One adapter serves the sync loop and the live subscription, so its
	// construction holds the lock: a second caller must not open another
	// connection for the same source.
	s.mu.Lock()
	defer s.mu.Unlock()
	if adapter, ok := s.perSource[key]; ok {
		return adapter, true
	}
	if s.factory == nil {
		return nil, false
	}
	adapter, ok, err := s.factory(source)
	if err != nil || !ok {
		return nil, false
	}
	s.perSource[key] = adapter
	return adapter, true
}

// Store is the registry and admission store the service reads into.
func (s *Service) Store() *Store { return s.store }

// Run reads every connected source until ctx ends. It never returns an error:
// a failing source is recorded in its own registry row.
func (s *Service) Run(ctx context.Context) {
	go s.RunLive(ctx)
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

// Sync reads one source: its chat list, then every chat that is in sync.
func (s *Service) Sync(ctx context.Context, source Source) (Result, error) {
	adapter, ok := s.adapterFor(source)
	if !ok {
		return Result{}, ErrInvalid
	}
	state, err := adapter.SignIn(ctx)
	if err != nil {
		return Result{}, err
	}
	if state == NeedsSignIn {
		if err := s.store.setState(ctx, source.Person, source.Platform, source.Account, StateNeedsSignIn, ""); err != nil {
			return Result{}, err
		}
		return Result{}, nil
	}
	chats, err := adapter.ListChats(ctx)
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
		page, err := adapter.Page(ctx, chat.Chat, cursor)
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
	return adapter.Live(ctx, deliver)
}

// failureCause keeps a short, bounded cause on the registry row.
func failureCause(err error) string {
	text := err.Error()
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}

// Apply admits one event the platform pushed: an edit or a new message becomes
// a record without moving the chat's cursor, and a deletion travels through the
// retraction path. An event for a chat this source does not hold is ignored.
func (s *Service) Apply(ctx context.Context, source Source, event Event) (Result, error) {
	var result Result
	switch {
	case event.Message != nil:
		if event.Chat == "" || !validName(event.Chat) {
			return result, ErrInvalid
		}
		outcome, err := s.store.AdmitMessages(ctx, source, event.Chat, []Message{*event.Message})
		result.Added, result.Updated, result.Waiting = outcome.Added, outcome.Updated, outcome.Waiting
		if errors.Is(err, ErrPageWaiting) {
			return result, nil
		}
		return result, err
	case event.Deletion != nil:
		deleted := 0
		var err error
		if event.Chat != "" {
			deleted, err = s.store.DeleteRecords(ctx, source, event.Chat, []string{event.Deletion.Message})
		} else {
			deleted, err = s.store.DeleteSourceMessages(ctx, source, []string{event.Deletion.Message})
		}
		result.Deleted = deleted
		return result, err
	}
	return result, ErrInvalid
}

// RunLive delivers pushed events for every connected source whose adapter has
// them, until ctx ends. A source whose adapter has no live events is read on
// its schedule only.
func (s *Service) RunLive(ctx context.Context) {
	subscribed := map[string]bool{}
	var mu sync.Mutex
	subscribe := func() {
		sources, err := s.store.allSources(ctx)
		if err != nil {
			return
		}
		for _, source := range sources {
			if source.State != StateConnected {
				continue
			}
			adapter, ok := s.adapterFor(source)
			if !ok || !adapter.Capabilities().LiveEvents {
				continue
			}
			key := source.Platform + "\x00" + source.Account
			mu.Lock()
			if subscribed[key] {
				mu.Unlock()
				continue
			}
			subscribed[key] = true
			mu.Unlock()
			go func(source Source, adapter Adapter) {
				_ = adapter.Live(ctx, func(event Event) error {
					_, _ = s.Apply(ctx, source, event)
					return nil
				})
				mu.Lock()
				delete(subscribed, key)
				mu.Unlock()
			}(source, adapter)
		}
	}
	subscribe()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			subscribe()
		}
	}
}
