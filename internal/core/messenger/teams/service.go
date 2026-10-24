// SPDX-License-Identifier: Apache-2.0
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// Connection contains public metadata only. Account becomes tenant:user after
// Graph identifies the signed-in account; it never comes from a client claim.
type Connection struct {
	ID       string    `json:"id"`
	Tenant   string    `json:"tenant"`
	ClientID string    `json:"client_id"`
	Account  string    `json:"account,omitempty"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Expires  time.Time `json:"expires"`
}
type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type Service struct {
	db        *canon.Store
	vault     *secrets.Store
	store     *messenger.Store
	person    string
	transport http.RoundTripper
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	attempts  map[string]attempt
	workers   map[string]worker
	closed    bool
	run       bool
}

func NewService(ctx context.Context, db *canon.Store, vault *secrets.Store, store *messenger.Store, person string, transport http.RoundTripper) *Service {
	ctx, cancel := context.WithCancel(ctx)
	return &Service{ctx: ctx, cancel: cancel, db: db, vault: vault, store: store, person: person, transport: transport, attempts: map[string]attempt{}, workers: map[string]worker{}, run: true}
}
func (s *Service) key(c Connection) secrets.Key {
	return secrets.Key{Person: s.person, Service: "messenger:teams:" + c.ID, Name: "tokens"}
}
func (s *Service) source(c Connection) messenger.Source {
	return messenger.Source{Person: s.person, Platform: "teams", Account: c.Account, Adapter: "teams", Mode: messenger.ModeOfficial, State: messenger.StateConnected}
}
func (s *Service) Connections(ctx context.Context) (out []Connection, err error) {
	out = []Connection{}
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM teams_connection WHERE person=? ORDER BY id", s.person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var c Connection
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if json.Unmarshal(raw, &c) != nil {
				return ErrInvalid
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}
func (s *Service) connection(ctx context.Context, id string) (Connection, error) {
	rows, err := s.Connections(ctx)
	if err != nil {
		return Connection{}, err
	}
	for _, c := range rows {
		if c.ID == id {
			return c, nil
		}
	}
	return Connection{}, ErrInvalid
}
func (s *Service) save(ctx context.Context, c Connection) error {
	raw, _ := json.Marshal(c)
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO teams_connection(person,id,record) VALUES(?,?,?) ON CONFLICT(person,id) DO UPDATE SET record=excluded.record", s.person, c.ID, raw)
		return err
	})
}

var guid = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
var connectionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

func validConfig(c Connection) bool {
	if !connectionID.MatchString(c.ID) || !guid.MatchString(c.Tenant) || !guid.MatchString(c.ClientID) || strings.EqualFold(c.Tenant, "9188040d-6c67-4c5b-b112-36a304b66dad") {
		return false
	}
	// These are Microsoft's Teams/Office/Graph tooling registrations, not the
	// person's public app. No built-in fallback client is ever supplied.
	for _, id := range []string{"1fec8e78-bce4-4aaf-ab1b-5451cc387264", "27922004-5251-4030-b22d-91ecd9a37ea4", "04b07795-8ddb-461a-bbee-02f9e1bf7b46", "d3590ed6-52b3-4102-aeff-aad2292ab01c", "14d82eec-204b-4c2f-b7e8-296a70dab67e"} {
		if strings.EqualFold(c.ClientID, id) {
			return false
		}
	}
	return true
}
func (s *Service) Configure(ctx context.Context, c Connection) error {
	if !validConfig(c) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrUnavailable
	}
	c.Tenant = strings.ToLower(c.Tenant)
	c.ClientID = strings.ToLower(c.ClientID)
	if old, err := s.connection(ctx, c.ID); err == nil {
		if err = s.disconnect(ctx, old); err != nil {
			return err
		}
	}
	c.Account = ""
	c.Status = "needs_sign_in"
	c.Reason = ""
	c.Expires = time.Time{}
	return s.save(ctx, c)
}
func (s *Service) stop(id string) {
	if w, ok := s.workers[id]; ok {
		w.cancel()
		<-w.done
		delete(s.workers, id)
	}
}
func (s *Service) disconnect(ctx context.Context, c Connection) error {
	s.stop(c.ID)
	for session, a := range s.attempts {
		if a.Connection.ID == c.ID {
			delete(s.attempts, session)
		}
	}
	return s.vault.DeleteTokens(ctx, s.key(c), func() error {
		c.Status = "disconnected"
		c.Reason = ""
		if err := s.save(ctx, c); err != nil {
			return err
		}
		if c.Account != "" {
			return s.store.Disconnect(ctx, s.person, "teams", c.Account)
		}
		return nil
	})
}
func (s *Service) Disconnect(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connection(ctx, id)
	if err != nil {
		return err
	}
	return s.disconnect(ctx, c)
}
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cancel()
	for id := range s.workers {
		s.stop(id)
	}
	clear(s.attempts)
}
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.Connections(ctx)
	if err != nil {
		return err
	}
	for _, c := range rows {
		if c.Status == "connected" || c.Status == "failing" {
			s.start(c)
		}
	}
	return nil
}
func (s *Service) start(c Connection) {
	if !s.run || s.closed {
		return
	}
	s.stop(c.ID)
	ctx, cancel := context.WithCancel(s.ctx)
	w := worker{cancel: cancel, done: make(chan struct{})}
	s.workers[c.ID] = w
	go func() {
		defer close(w.done)
		adapter := NewAdapter(Config{Tenant: c.Tenant, Account: c.Account, Transport: s.transport, Token: func(ctx context.Context, force bool) (secret.Secret, error) { return s.token(ctx, c.ID, force) }})
		syncer := messenger.NewService(s.store, map[string]messenger.Adapter{"teams": adapter}, 0)
		for ctx.Err() == nil {
			delay := time.Minute
			_, err := syncer.Sync(ctx, s.source(c))
			if ctx.Err() != nil {
				return
			}
			current, loadErr := s.connection(ctx, c.ID)
			if loadErr != nil {
				return
			}
			if err != nil {
				current.Status = "failing"
				current.Reason = ErrUnavailable.Error()
				var retry *RetryError
				if errors.As(err, &retry) {
					delay = max(delay, retry.After)
					current.Reason = retry.Error()
				}
				if errors.Is(err, ErrSignIn) {
					current.Status = "needs_sign_in"
					current.Reason = ErrSignIn.Error()
				}
				if errors.Is(err, ErrUnsupported) {
					current.Status = "unsupported"
					current.Reason = ErrUnsupported.Error()
				}
				_ = s.store.MarkFailure(ctx, s.person, "teams", c.Account, current.Reason)
			} else {
				current.Status = "connected"
				current.Reason = ""
				// A people record added after these messages arrived links on
				// the next pass, as it does on Core's own messenger loop.
				_, _ = s.store.Rematch(ctx)
			}
			if s.save(ctx, current) != nil {
				return
			}
			if current.Status == "unsupported" || current.Status == "needs_sign_in" {
				return
			}
			if pause(ctx, delay) != nil {
				return
			}
		}
	}()
}
