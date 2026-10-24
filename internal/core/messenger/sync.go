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
	resolve  Resolver
	factory  Factory
	interval time.Duration

	admission  sync.Map // SourceID -> chan struct{}; never held during a platform read.
	activity   sync.RWMutex
	wake       chan struct{}
	liveWake   chan struct{}
	liveCancel map[string]context.CancelFunc
	mu         sync.Mutex
	lastSync   map[string]time.Time
	perSource  map[string]Adapter
	syncReads  map[string]map[*sourceRead]struct{}
	stopping   map[string]bool
}

type sourceRead struct{ cancel context.CancelFunc }

// Factory opens the adapter of one source. It returns false when it does not
// serve that source. Core uses it for account-specific credentials.
type Factory func(Source) (Adapter, bool, error)

// NewService composes the sync loop. adapters are keyed by the source's adapter
// name; factory opens account-specific adapters. An unsupported source is left
// untouched.
func NewService(store *Store, adapters map[string]Adapter, interval time.Duration) *Service {
	return NewServiceWithResolver(store, func(source Source) (Adapter, bool) {
		adapter, ok := adapters[source.Adapter]
		return adapter, ok
	}, interval)
}

// Resolver selects an adapter for the source account.
type Resolver func(Source) (Adapter, bool)

// NewServiceWithResolver supports account-specific sessions while NewService
// retains the registry of adapter names used by official platform services.
func NewServiceWithResolver(store *Store, resolve Resolver, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Service{store: store, resolve: resolve, interval: interval, wake: make(chan struct{}, 1), liveWake: make(chan struct{}, 1), liveCancel: map[string]context.CancelFunc{}, lastSync: map[string]time.Time{}, perSource: map[string]Adapter{}, syncReads: map[string]map[*sourceRead]struct{}{}, stopping: map[string]bool{}}
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
	if adapter, ok := s.resolve(source); ok {
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
	liveDone := make(chan struct{})
	go func() { defer close(liveDone); s.RunLive(ctx) }()
	defer func() { <-liveDone }()
	s.SyncAll(ctx, nil)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.SyncAll(ctx, nil)
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
		if source.State != StateConnected && source.State != StateFailing {
			continue
		}
		if _, ok := s.adapterFor(source); !ok {
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
		if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			s.activity.RLock()
			if s.connected(ctx, source) {
				_ = s.store.MarkFailure(ctx, source.Person, source.Platform, source.Account, failureCause(err))
			}
			s.activity.RUnlock()
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

func sourceID(source Source) SourceID {
	return SourceID{Platform: source.Platform, Account: source.Account}
}

// Sync reads one source: its chat list, then every chat that is in sync.
func (s *Service) Sync(ctx context.Context, source Source) (result Result, err error) {
	s.activity.RLock()
	defer s.activity.RUnlock()
	if !s.connected(ctx, source) {
		return Result{}, nil
	}
	key := source.Platform + "\x00" + source.Account
	s.mu.Lock()
	if s.stopping[key] {
		s.mu.Unlock()
		return Result{}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	read := &sourceRead{cancel: cancel}
	if s.syncReads[key] == nil {
		s.syncReads[key] = map[*sourceRead]struct{}{}
	}
	s.syncReads[key][read] = struct{}{}
	s.mu.Unlock()
	defer func() {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if errors.Is(err, ErrNeedsSignIn) {
			err = s.store.setState(ctx, source.Person, source.Platform, source.Account, StateNeedsSignIn, "")
		}
		cancel()
		s.mu.Lock()
		delete(s.syncReads[key], read)
		if len(s.syncReads[key]) == 0 {
			delete(s.syncReads, key)
		}
		s.mu.Unlock()
	}()
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
	if recovery, ok := adapter.(interface {
		Recover(context.Context, SourceID, func(Event) error) error
	}); ok {
		if err := recovery.Recover(ctx, id, func(event Event) error {
			_, err := s.apply(ctx, source, event)
			return err
		}); err != nil {
			return result, err
		}
	}
	records, err := s.store.Chats(ctx, source.Person, source.Platform, source.Account)
	if err != nil {
		return Result{}, err
	}
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
		outcome, err := s.admitPage(ctx, source, chat.Chat, cursor, page)
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

// lockAdmission serializes revision planning and commit for one source. Live
// updates and history pages must observe each other's completed revisions.
func (s *Service) lockAdmission(ctx context.Context, source Source) (func(), error) {
	value, _ := s.admission.LoadOrStore(sourceID(source), make(chan struct{}, 1))
	token := value.(chan struct{})
	select {
	case token <- struct{}{}:
		return func() { <-token }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) admitPage(ctx context.Context, source Source, chat string, cursor Cursor, page Page) (PageResult, error) {
	unlock, err := s.lockAdmission(ctx, source)
	if err != nil {
		return PageResult{}, err
	}
	defer unlock()
	return s.store.AdmitPage(ctx, source, chat, cursor, page)
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

// Apply admits one event the platform pushed: an edit or a new message becomes
// a record without moving the chat's cursor, and a deletion travels through the
// retraction path. An event for a chat this source does not hold is ignored.
func (s *Service) Apply(ctx context.Context, source Source, event Event) (Result, error) {
	s.activity.RLock()
	defer s.activity.RUnlock()
	return s.apply(ctx, source, event)
}

// apply also serves recovery while Sync already holds the activity lock.
func (s *Service) apply(ctx context.Context, source Source, event Event) (Result, error) {
	if !s.connected(ctx, source) {
		return Result{}, nil
	}
	unlock, err := s.lockAdmission(ctx, source)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	if event.Message != nil {
		if event.Chat == "" || !validName(event.Chat) {
			return Result{}, ErrInvalid
		}
		chats, err := s.store.Chats(ctx, source.Person, source.Platform, source.Account)
		if err != nil {
			return Result{}, err
		}
		allowed := false
		for _, chat := range chats {
			if chat.Chat == event.Chat && chat.Sync {
				allowed = true
			}
		}
		if !allowed {
			return Result{}, nil
		}
	}
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
			deleted, err = s.store.DeleteSourceMessages(ctx, source, []string{event.Deletion.Message}, event.ChatPrefixes...)
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
	var workers sync.WaitGroup
	defer workers.Wait()
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
			liveCtx, cancel := context.WithCancel(ctx)
			s.mu.Lock()
			s.liveCancel[key] = cancel
			s.mu.Unlock()
			workers.Add(1)
			go func(source Source, adapter Adapter) {
				defer workers.Done()
				defer cancel()
				_ = adapter.Live(liveCtx, sourceID(source), func(event Event) error {
					_, err := s.Apply(liveCtx, source, event)
					return err
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
		case <-s.liveWake:
			subscribe()
		case <-ticker.C:
			subscribe()
		}
	}
}

// Wake schedules newly connected sources without waiting for the next tick.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
	select {
	case s.liveWake <- struct{}{}:
	default:
	}
}
func (s *Service) connected(ctx context.Context, source Source) bool {
	rows, err := s.store.Sources(ctx, source.Person)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.Platform == source.Platform && row.Account == source.Account {
			return row.State == StateConnected || row.State == StateFailing
		}
	}
	return false
}

// Disconnect stops reading before removing the source's authentication.
func (s *Service) Disconnect(ctx context.Context, source Source) error {
	key := source.Platform + "\x00" + source.Account
	// Cancel network reads and rate-limit waits before waiting for their writes.
	s.mu.Lock()
	s.stopping[key] = true
	for read := range s.syncReads[key] {
		read.cancel()
	}
	if cancel := s.liveCancel[key]; cancel != nil {
		cancel()
		delete(s.liveCancel, key)
	}
	s.mu.Unlock()
	s.activity.Lock()
	defer s.activity.Unlock()
	defer func() { s.mu.Lock(); delete(s.stopping, key); s.mu.Unlock() }()
	s.mu.Lock()
	adapter := s.perSource[key]
	delete(s.perSource, key)
	delete(s.lastSync, key)
	s.mu.Unlock()
	if closer, ok := adapter.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return s.store.Disconnect(ctx, source.Person, source.Platform, source.Account)
}
