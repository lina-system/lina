// SPDX-License-Identifier: Apache-2.0

package webex

import (
	"context"
	"sync"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// Service owns one person's integration, connection-bound sign-ins and reader.
// Control operations stop the reader before replacing or deleting credentials.
type Service struct {
	ctx      context.Context
	store    *messenger.Store
	cfg      Config
	mu       sync.Mutex
	attempts map[string]*Adapter
	stop     context.CancelFunc
	done     chan struct{}
	closed   bool
}

func NewService(ctx context.Context, store *messenger.Store, cfg Config) *Service {
	return &Service{ctx: ctx, store: store, cfg: cfg, attempts: map[string]*Adapter{}}
}
func (s *Service) Sources(ctx context.Context) ([]messenger.Source, error) {
	all, err := s.store.Sources(ctx, s.cfg.Person)
	out := []messenger.Source{}
	for _, source := range all {
		if source.Platform == Platform {
			out = append(out, source)
		}
	}
	return out, err
}
func (s *Service) active(ctx context.Context) (string, error) {
	rows, err := s.Sources(ctx)
	if err != nil {
		return "", err
	}
	account := ""
	for _, source := range rows {
		if source.State == messenger.StateDisconnected {
			continue
		}
		if account != "" {
			return "", messenger.ErrInvalid
		}
		account = source.Account
	}
	return account, nil
}
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.start(ctx)
}
func (s *Service) start(ctx context.Context) error {
	if s.closed || s.stop != nil {
		return nil
	}
	account, err := s.active(ctx)
	if err != nil || account == "" {
		return err
	}
	cfg := s.cfg
	cfg.Account = account
	adapter, err := New(cfg)
	if err != nil {
		return err
	}
	run, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.stop, s.done = cancel, done
	loop := messenger.NewService(s.store, map[string]messenger.Adapter{Platform: adapter}, adapter.Capabilities().Interval)
	go func() { defer close(done); loop.Run(run) }()
	return nil
}
func (s *Service) stopReader() {
	if s.stop != nil {
		s.stop()
		<-s.done
		s.stop = nil
		s.done = nil
	}
}
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	clear(s.attempts)
	s.stopReader()
}
func (s *Service) Configure(ctx context.Context, id, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return messenger.ErrNotConnected
	}
	account, err := s.active(ctx)
	if err != nil {
		return err
	}
	if account != "" {
		return messenger.ErrInvalid
	}
	clear(s.attempts)
	adapter, err := New(s.cfg)
	if err != nil {
		return err
	}
	return adapter.Configure(ctx, id, secret)
}
func (s *Service) Begin(ctx context.Context, connection string, port int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || connection == "" {
		return "", messenger.ErrInvalid
	}
	// A single integration accepts one in-flight consent. Another connection
	// cannot overwrite its verifier or consume its callback.
	for key, adapter := range s.attempts {
		if adapter.pending == nil || !adapter.now().Before(adapter.pending.Expires) {
			delete(s.attempts, key)
		}
	}
	if len(s.attempts) != 0 {
		return "", messenger.ErrInvalid
	}
	account, err := s.active(ctx)
	if err != nil {
		return "", err
	}
	cfg := s.cfg
	cfg.Account = account
	adapter, err := New(cfg)
	if err != nil {
		return "", err
	}
	target, err := adapter.BeginSignIn(ctx, port)
	if err == nil {
		s.attempts[connection] = adapter
	}
	return target, err
}
func (s *Service) Cancel(connection string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, connection)
}
func (s *Service) Complete(ctx context.Context, connection string, redirect Redirect) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	adapter := s.attempts[connection]
	delete(s.attempts, connection)
	if s.closed || adapter == nil {
		return Account{}, messenger.ErrNeedsSignIn
	}
	s.stopReader()
	defer func() { _ = s.start(s.ctx) }()
	account, err := adapter.CompleteSignIn(ctx, redirect)
	if err != nil {
		return Account{}, err
	}
	err = s.store.Connect(ctx, messenger.Source{Person: s.cfg.Person, Platform: Platform, Account: account.ID, Adapter: Platform, Mode: messenger.ModeOfficial, State: messenger.StateConnected})
	return account, err
}
func (s *Service) Disconnect(ctx context.Context, account string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return messenger.ErrNotConnected
	}
	rows, err := s.Sources(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, source := range rows {
		if source.Account == account {
			found = true
		}
	}
	if !found {
		return messenger.ErrNotFound
	}
	active, err := s.active(ctx)
	if err != nil {
		return err
	}
	// Disconnecting an old retained source must not delete a new account's token.
	if active != "" && active != account {
		return messenger.ErrInvalid
	}
	clear(s.attempts)
	s.stopReader()
	if err := s.store.Disconnect(ctx, s.cfg.Person, Platform, account); err != nil {
		return err
	}
	adapter, err := New(s.cfg)
	if err != nil {
		return err
	}
	return adapter.Forget()
}
