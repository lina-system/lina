// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrSignInBusy = errors.New("sign_in_capacity_reached")

// Pending is the only authorization state returned to the client. The URL
// contains the public challenge; the verifier and expected nonce stay in Core.
type Pending struct {
	ID      string    `json:"pending_id"`
	URL     string    `json:"authorization_url"`
	Expires time.Time `json:"expires_at"`
}

type signInSelection struct {
	accountID, clientID, subject string
	login                        bool
}
type pendingSignIn struct {
	connection    string
	authorization authorization
	selected      signInSelection
	ctx           context.Context
	cancel        context.CancelFunc
	consumed      bool
}
type pendingSignIns struct {
	mu       sync.Mutex
	attempts map[string]*pendingSignIn
	now      func() time.Time
	exchange func(context.Context, authorization, Relayed) (authorizedTokens, error)
}

func newPendingSignIns(now func() time.Time, exchange func(context.Context, authorization, Relayed) (authorizedTokens, error)) *pendingSignIns {
	return &pendingSignIns{attempts: make(map[string]*pendingSignIn), now: now, exchange: exchange}
}
func (s *pendingSignIns) begin(ctx context.Context, connection, hostID string, selected signInSelection, port int) (Pending, error) {
	if connection == "" {
		return Pending{}, ErrSignInInvalid
	}
	if err := ctx.Err(); err != nil {
		return Pending{}, err
	}
	now := s.now()
	a, err := newAuthorization(hostID, selected.clientID, selected.subject, port, now)
	if err != nil {
		return Pending{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, attempt := range s.attempts {
		if attempt.connection == connection || !now.Before(attempt.authorization.expires) || attempt.ctx.Err() != nil {
			attempt.cancel()
			delete(s.attempts, id)
		}
	}
	if len(s.attempts) >= 64 {
		return Pending{}, ErrSignInBusy
	}
	lifetime, cancel := context.WithCancel(ctx)
	id := randomSignInID()
	s.attempts[id] = &pendingSignIn{connection: connection, authorization: a, selected: selected, ctx: lifetime, cancel: cancel}
	return Pending{ID: id, URL: a.url, Expires: a.expires}, nil
}
func (s *pendingSignIns) complete(ctx context.Context, connection, id string, in Relayed) (authorizedTokens, signInSelection, error) {
	s.mu.Lock()
	attempt, ok := s.attempts[id]
	if !ok || attempt.connection != connection || attempt.consumed {
		s.mu.Unlock()
		return authorizedTokens{}, signInSelection{}, ErrSignInInvalid
	}
	attempt.consumed = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.attempts, id); s.mu.Unlock(); attempt.cancel() }()
	if !s.now().Before(attempt.authorization.expires) {
		return authorizedTokens{}, signInSelection{}, ErrSignInExpired
	}
	if err := attempt.ctx.Err(); err != nil {
		return authorizedTokens{}, signInSelection{}, err
	}
	exchangeCtx, cancel := context.WithCancel(attempt.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return authorizedTokens{}, signInSelection{}, err
	}
	tokens, err := s.exchange(exchangeCtx, attempt.authorization, in)
	if err == nil {
		err = exchangeCtx.Err()
	}
	if err != nil {
		return authorizedTokens{}, signInSelection{}, err
	}
	return tokens, attempt.selected, nil
}
func (s *pendingSignIns) disconnect(connection string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, attempt := range s.attempts {
		if attempt.connection == connection {
			attempt.cancel()
			delete(s.attempts, id)
		}
	}
}
