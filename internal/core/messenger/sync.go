// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"errors"
	"fmt"
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
	interval time.Duration

	mu       sync.Mutex
	lastSync map[string]time.Time
	// live holds one subscription per source account, so a platform sees one
	// session. A source that stops being connected has its subscription
	// cancelled. token identifies the current subscription, so a finishing one
	// never removes its replacement.
	live     map[string]liveSub
	liveWait sync.WaitGroup
	sequence uint64
}

type liveSub struct {
	stop  context.CancelFunc
	token uint64
}

// NewService composes the sync loop. adapters are keyed by the adapter name a
// source records. A platform with one adapter uses the platform name; Discord's
// bot and personal account register two names. A source whose name is absent
// is left untouched.
func NewService(store *Store, adapters map[string]Adapter, interval time.Duration) *Service {
	if interval <= 0 {
		interval = DefaultInterval
	}
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
	return &Service{store: store, resolve: resolve, interval: interval, lastSync: map[string]time.Time{}, live: map[string]liveSub{}}
}

// Store is the registry and admission store the service reads into.
func (s *Service) Store() *Store { return s.store }

// Run reads every connected source and follows the live events of each one that
// has them, until ctx ends. It never returns an error: a failing source is
// recorded in its own registry row.
func (s *Service) Run(ctx context.Context) {
	s.SyncAll(ctx, nil)
	s.reconcileLive(ctx)
	scheduled := time.NewTicker(s.interval)
	defer scheduled.Stop()
	follow := time.NewTicker(s.interval)
	defer follow.Stop()
	for {
		select {
		case <-ctx.Done():
			// A subscription is joined before the caller may close the store
			// under it.
			s.stopLive()
			s.liveWait.Wait()
			return
		case <-scheduled.C:
			s.SyncAll(ctx, nil)
		case <-follow.C:
			s.reconcileLive(ctx)
		}
	}
}

func liveKey(source Source) string { return source.Platform + "\x00" + source.Account }

// reconcileLive keeps one live subscription per connected source that offers
// live events. A subscription whose source is no longer connected is
// cancelled; a platform whose subscription ends is retried on the next
// interval, and one without live events is never started.
func (s *Service) reconcileLive(ctx context.Context) {
	sources, err := s.store.allSources(ctx)
	if err != nil {
		return
	}
	wanted := map[string]Source{}
	for _, source := range sources {
		if source.State != StateConnected {
			continue
		}
		adapter, ok := s.adapterFor(source)
		if !ok || !adapter.Capabilities().LiveEvents {
			continue
		}
		wanted[liveKey(source)] = source
	}
	s.mu.Lock()
	for key, sub := range s.live {
		if _, keep := wanted[key]; !keep {
			sub.stop()
			delete(s.live, key)
		}
	}
	for key, source := range wanted {
		if _, running := s.live[key]; running {
			continue
		}
		run, stop := context.WithCancel(ctx)
		s.sequence++
		token := s.sequence
		s.live[key] = liveSub{stop: stop, token: token}
		s.liveWait.Add(1)
		go func() {
			defer s.liveWait.Done()
			s.watch(run, source, token)
		}()
	}
	s.mu.Unlock()
}

func (s *Service) stopLive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sub := range s.live {
		sub.stop()
		delete(s.live, key)
	}
}

// watch follows one source's live events until the subscription ends. A
// subscription that fails names the cause on that source and leaves the next
// interval to retry it; every other source and the conversation keep working.
func (s *Service) watch(ctx context.Context, source Source, token uint64) {
	adapter, ok := s.adapterFor(source)
	if !ok {
		return
	}
	err := adapter.Live(ctx, sourceID(source), func(event Event) error {
		return s.deliverLive(ctx, source, adapter, event)
	})
	s.mu.Lock()
	if current, ok := s.live[liveKey(source)]; ok && current.token == token {
		delete(s.live, liveKey(source))
	}
	s.mu.Unlock()
	if err != nil && ctx.Err() == nil {
		_ = s.store.MarkFailure(context.WithoutCancel(ctx), source.Person, source.Platform, source.Account, failureCause(err))
	}
}

// deliverLive admits one pushed event. A chat the source has not listed yet is
// listed first; if it is still absent the event is dropped, because the next
// page read of that chat covers it before the cursor moves past it.
func (s *Service) deliverLive(ctx context.Context, source Source, adapter Adapter, event Event) error {
	if event.Chat == "" || (event.Message == nil) == (event.Deletion == nil) {
		return fmt.Errorf("%w: live event", ErrInvalid)
	}
	listed, inSync, err := s.store.ChatSync(ctx, source, event.Chat)
	if err != nil {
		return err
	}
	if !listed {
		chats, err := adapter.ListChats(ctx, sourceID(source))
		if err != nil {
			return err
		}
		if err := s.store.UpsertChats(ctx, source, chats); err != nil {
			return err
		}
		if listed, inSync, err = s.store.ChatSync(ctx, source, event.Chat); err != nil {
			return err
		}
	}
	if !listed || !inSync {
		return nil
	}
	_, err = s.store.AdmitLive(ctx, source, event)
	if errors.Is(err, ErrPageWaiting) {
		// The scheduled read holds the page until the write settles; its cursor
		// has not moved, so the message is read again then.
		return nil
	}
	return err
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
	return s.resolve(source)
}

func sourceID(source Source) SourceID {
	return SourceID{Platform: source.Platform, Account: source.Account}
}

// Sync reads one source: its chat list, then every chat that is in sync.
func (s *Service) Sync(ctx context.Context, source Source) (result Result, err error) {
	defer func() {
		if errors.Is(err, ErrNeedsSignIn) {
			err = s.store.setState(ctx, source.Person, source.Platform, source.Account, StateNeedsSignIn, "")
		}
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
