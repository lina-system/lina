// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// DefaultInterval is the schedule for a source whose adapter states none.
const DefaultInterval = time.Minute

// liveBackoff bounds how long a failing live reader waits before it retries.
const (
	liveBackoffMin = time.Second
	liveBackoffMax = time.Minute
)

// Service reads connected sources on their schedule and from their live events.
// One source's failure never blocks another source or the conversation.
type Service struct {
	store    *Store
	adapters func(Source) (Adapter, error)
	interval time.Duration

	mu       sync.Mutex
	lastSync map[string]time.Time
	// resolved caches one adapter per source account, so a schedule and a live
	// reader share it and its connections.
	resolved map[string]Adapter
	// watching holds each source's live reader, so a source connected while
	// Core runs gains one at its next schedule and a disconnected source stops
	// its reader at once.
	watching map[string]*watcher
}

// watcher is one source's live reader.
type watcher struct {
	cancel context.CancelFunc
}

// NewService composes the sync loop. adapters are keyed by the adapter name a
// source records. A platform with one adapter uses the platform name; Discord's
// bot and personal account register two names. A source whose name is absent
// is left untouched.
func NewService(store *Store, adapters map[string]Adapter, interval time.Duration) *Service {
	return NewServiceWith(store, func(source Source) (Adapter, error) {
		adapter, ok := adapters[source.Adapter]
		if !ok {
			return nil, ErrInvalid
		}
		return adapter, nil
	}, interval)
}

// NewServiceWith composes the sync loop with a provider that resolves the
// adapter for one source account, so two sources on one platform never share an
// adapter instance.
func NewServiceWith(store *Store, adapters func(Source) (Adapter, error), interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if adapters == nil {
		adapters = func(Source) (Adapter, error) { return nil, ErrInvalid }
	}
	return &Service{store: store, adapters: adapters, interval: interval, lastSync: map[string]time.Time{}, watching: map[string]*watcher{}, resolved: map[string]Adapter{}}
}

// adapter returns the source's adapter, creating it once.
func (s *Service) adapter(source Source) (Adapter, error) {
	key := source.Platform + "\x00" + source.Account
	s.mu.Lock()
	adapter, ok := s.resolved[key]
	s.mu.Unlock()
	if ok {
		return adapter, nil
	}
	adapter, err := s.adapters(source)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if existing, ok := s.resolved[key]; ok {
		adapter = existing
	} else {
		s.resolved[key] = adapter
	}
	s.mu.Unlock()
	return adapter, nil
}

// Close releases the adapters the provider created.
func (s *Service) Close() {
	s.mu.Lock()
	adapters := s.resolved
	s.resolved = map[string]Adapter{}
	s.mu.Unlock()
	for _, adapter := range adapters {
		closeAdapter(adapter)
	}
}

// closeAdapter supports the cleanup signatures of the platform adapters.
func closeAdapter(adapter Adapter) {
	switch closer := adapter.(type) {
	case io.Closer:
		_ = closer.Close()
	case interface{ Close() }:
		closer.Close()
	}
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
	connected := make(map[string]bool, len(sources))
	defer func() { s.retain(connected) }()
	for _, source := range sources {
		// A failing source keeps its schedule: the cause may be a credential or
		// an unreachable node that the person can fix. A source that needs a
		// new sign-in waits for the person instead.
		if source.State != StateConnected && source.State != StateFailing {
			continue
		}
		key := source.Platform + "\x00" + source.Account
		connected[key] = true
		if source.State == StateConnected {
			s.watch(ctx, source)
		}
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

// watch starts a source's live reader once, when its adapter pushes events.
func (s *Service) watch(ctx context.Context, source Source) {
	key := source.Platform + "\x00" + source.Account
	s.mu.Lock()
	if s.watching[key] != nil {
		s.mu.Unlock()
		return
	}
	reader, cancel := context.WithCancel(ctx)
	w := &watcher{cancel: cancel}
	s.watching[key] = w
	s.mu.Unlock()
	go func() {
		// Observe committed registry changes independently of scheduled network I/O.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				changed := s.store.db.Changed()
				connected, err := s.store.liveConnected(reader, source)
				if err != nil || !connected {
					cancel()
					return
				}
				select {
				case <-reader.Done():
					return
				case <-changed:
				}
			}
		}()
		s.follow(reader, source, key, w)
		cancel()
		<-done
	}()
}

// retain stops the live readers of sources that are no longer connected and
// releases their adapters. Disconnecting a source stops its sync at once.
func (s *Service) retain(connected map[string]bool) {
	s.mu.Lock()
	var stopped []*watcher
	var released []Adapter
	for key, w := range s.watching {
		if !connected[key] {
			stopped = append(stopped, w)
			delete(s.watching, key)
		}
	}
	for key, adapter := range s.resolved {
		if !connected[key] {
			released = append(released, adapter)
			delete(s.resolved, key)
		}
	}
	s.mu.Unlock()
	for _, w := range stopped {
		w.cancel()
	}
	for _, adapter := range released {
		closeAdapter(adapter)
	}
}

// follow reads one source's live events until ctx ends, and after a failure
// backs off and rejoins that platform's session.
func (s *Service) follow(ctx context.Context, source Source, key string, w *watcher) {
	defer func() {
		s.mu.Lock()
		if s.watching[key] == w {
			delete(s.watching, key)
		}
		s.mu.Unlock()
	}()
	backoff := liveBackoffMin
	for ctx.Err() == nil {
		adapter, err := s.adapter(source)
		if err != nil {
			return
		}
		if !adapter.Capabilities().LiveEvents {
			return
		}
		err = adapter.Live(ctx, sourceID(source), func(event Event) error {
			_, err := s.store.AdmitEvent(ctx, source, event)
			if errors.Is(err, ErrPageWaiting) {
				// The page read admits the event once the chat's write settles.
				return nil
			}
			return err
		})
		if ctx.Err() != nil || errors.Is(err, ErrNotConnected) {
			return
		}
		if err != nil && !errors.Is(err, ErrPageWaiting) {
			_ = s.store.MarkFailure(ctx, source.Person, source.Platform, source.Account, failureCause(err))
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(2*backoff, liveBackoffMax)
	}
}

func (s *Service) intervalOf(source Source) time.Duration {
	if adapter, err := s.adapter(source); err == nil {
		if interval := adapter.Capabilities().Interval; interval > 0 {
			return interval
		}
	}
	return s.interval
}

func sourceID(source Source) SourceID {
	return SourceID{Platform: source.Platform, Account: source.Account}
}

// Sync reads one source: its chat list, then every chat that is in sync.
func (s *Service) Sync(ctx context.Context, source Source) (Result, error) {
	adapter, err := s.adapter(source)
	if err != nil {
		return Result{}, err
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
	s.watch(ctx, source)
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
	adapter, err := s.adapter(source)
	if err != nil {
		return err
	}
	return adapter.Live(ctx, sourceID(source), deliver)
}

// failureCause keeps a short, bounded cause on the registry row.
func failureCause(err error) string {
	if errors.Is(err, ErrTextUnavailable) {
		return "message text is unavailable: the bot's message-content intent is off"
	}
	if errors.Is(err, ErrNotConnected) {
		return "the bot token for this account is not stored"
	}
	text := err.Error()
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}
