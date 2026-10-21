// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// SignInSchema is included at its fixed version in canon.Migrations. Sign-in
// never creates tables on demand or opens another canonical writer.
const SignInSchema = canon.AccountSignInSchema

type SignInConfig struct {
	Store     *Store
	Person    string
	Leaser    lease.Leaser
	Transport http.RoundTripper
}

type SignIn struct {
	store          *Store
	person, hostID string
	pending        *pendingSignIns
}

func NewSignIn(ctx context.Context, cfg SignInConfig) (*SignIn, error) {
	if cfg.Store == nil || !textValid(cfg.Person) || cfg.Leaser == nil {
		return nil, ErrSignInInvalid
	}
	s := &SignIn{store: cfg.Store, person: cfg.Person}
	err := s.store.db.Write(ctx, func(tx *canon.Tx) error {
		var bytes [16]byte
		_, _ = rand.Read(bytes[:])
		bytes[6] = (bytes[6] & 0x0f) | 0x40
		bytes[8] = (bytes[8] & 0x3f) | 0x80
		candidate := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
		if _, err := tx.ExecContext(ctx, "INSERT INTO sign_in_host(singleton,host_id) VALUES(1,?) ON CONFLICT(singleton) DO NOTHING", candidate); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT host_id FROM sign_in_host WHERE singleton=1").Scan(&s.hostID)
	})
	if err != nil {
		return nil, ErrStorage
	}
	s.pending = newPendingSignIns(s.store.now, func(ctx context.Context, a authorization, r Relayed) (authorizedTokens, error) {
		activity, err := cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "ChatGPT sign-in"})
		if err != nil {
			return authorizedTokens{}, ErrSignInUnavailable
		}
		defer activity.Release()
		var tokens authorizedTokens
		err = lease.Do(ctx, activity, func(ctx context.Context) error {
			var err error
			tokens, err = exchangeAuthorization(ctx, cfg.Transport, a, r, s.store.now())
			return err
		})
		return tokens, err
	})
	return s, nil
}

func (s *SignIn) Begin(ctx context.Context, p provider.ProviderID, r RelayCaps) (Pending, error) {
	if s.store.ctx.Err() != nil {
		return Pending{}, ErrClosed
	}
	var registration *signInRegistration
	if r.AccountID != "" {
		saved, err := s.store.registration(ctx, r.AccountID)
		if err != nil {
			return Pending{}, err
		}
		registration = &saved
	}
	return s.pending.beginRelay(ctx, p, s.person, s.hostID, r, registration)
}

func (s *SignIn) Complete(ctx context.Context, id string, in Relayed) (Account, error) {
	if s.store.ctx.Err() != nil {
		return Account{}, ErrClosed
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.store.ctx, cancel)
	defer stop()
	tokens, selected, err := s.pending.complete(lifetime, in.ConnectionID, id, in)
	if err != nil {
		return Account{}, err
	}
	return s.store.admitSignIn(lifetime, s.person, s.hostID, selected, tokens)
}

func (s *SignIn) Disconnect(connectionID string) { s.pending.disconnect(connectionID) }
func (s *SignIn) Close() {
	s.pending.mu.Lock()
	defer s.pending.mu.Unlock()
	s.pending.closed = true
	for id, attempt := range s.pending.attempts {
		attempt.cancel()
		delete(s.pending.attempts, id)
	}
}

func (s *Store) registration(ctx context.Context, id provider.AccountID) (signInRegistration, error) {
	var r signInRegistration
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		a, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		r = signInRegistration{accountID: a.ID, person: a.Person, provider: a.Provider, clientID: a.ClientID, hostID: a.HostID}
		return tx.QueryRowContext(ctx, "SELECT issuer,subject FROM sign_in_identities WHERE account_id=?", id).Scan(&r.issuer, &r.subject)
	})
	if err != nil {
		return signInRegistration{}, ErrSignInInvalid
	}
	return r, nil
}

func verifiedAccount(id provider.AccountID, person, hostID string, t authorizedTokens, now time.Time) Account {
	refreshExpires := now.Add(30 * 24 * time.Hour)
	return Account{ID: id, Person: person, Provider: provider.ChatGPT, Label: "ChatGPT", Auth: provider.LoopbackOAuth, ClientID: t.clientID, HostID: hostID, Scopes: t.scopes, AccessExpires: &t.accessExpires, RefreshExpires: &refreshExpires, Status: OK, LastSuccess: now}
}
