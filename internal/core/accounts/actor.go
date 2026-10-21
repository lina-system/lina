// SPDX-License-Identifier: Apache-2.0
package accounts

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
)

type result struct {
	credential provider.Credential
	err        error
}
type request struct {
	ctx   context.Context
	fn    func(Account) (provider.Credential, error)
	reply chan result
}
type actor struct{ requests chan request }

func (s *Store) start(id provider.AccountID) {
	a := &actor{requests: make(chan request)}
	s.actors[id] = a
	s.wg.Go(func() { s.run(id, a) })
}
func (s *Store) call(ctx context.Context, id provider.AccountID, fn func(Account) (provider.Credential, error)) (provider.Credential, error) {
	s.mu.Lock()
	a := s.actors[id]
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return provider.Credential{}, ErrClosed
	}
	if a == nil {
		return provider.Credential{}, ErrNotFound
	}
	r := request{ctx: ctx, fn: fn, reply: make(chan result, 1)}
	select {
	case <-ctx.Done():
		return provider.Credential{}, ctx.Err()
	case <-s.ctx.Done():
		return provider.Credential{}, ErrClosed
	case a.requests <- r:
	}
	select {
	case <-ctx.Done():
		return provider.Credential{}, ctx.Err()
	case <-s.ctx.Done():
		return provider.Credential{}, ErrClosed
	case out := <-r.reply:
		return out.credential, out.err
	}
}
func (s *Store) run(id provider.AccountID, actor *actor) {
	// A storage failure latches this actor: do not spin a timer or release a
	// credential if the durable status cannot be established.
	var fatal error
	var retryAt time.Time
	for {
		a, err := s.Get(s.ctx, id)
		if err != nil {
			fatal = err
		}
		var wake <-chan time.Time
		var timer *time.Timer
		if fatal == nil && a.Status != NeedsSignIn && a.Auth == provider.LoopbackOAuth {
			next := dueAt(a)
			if retryAt.After(next) {
				next = retryAt
			}
			timer = time.NewTimer(next.Sub(s.now()))
			wake = timer.C
		}
		select {
		case <-s.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case r := <-actor.requests:
			if timer != nil {
				timer.Stop()
			}
			if err := r.ctx.Err(); err != nil {
				r.reply <- result{err: err}
				continue
			}
			if fatal != nil {
				r.reply <- result{err: fatal}
				continue
			}
			// Read after admission, so a persisted status is never bypassed by a
			// snapshot loaded before the request arrived.
			a, err = s.Get(s.ctx, id)
			if err != nil {
				fatal = err
				r.reply <- result{err: err}
				continue
			}
			c, err := r.fn(a)
			if err == ErrUnavailable {
				retryAt = s.now().Add(time.Minute)
			} else {
				retryAt = time.Time{}
			}
			if err == ErrStorage {
				fatal = err
			}
			r.reply <- result{c, err}
		case <-wake:
			a, err = s.Get(s.ctx, id)
			if err == nil && a.Status != NeedsSignIn {
				err = s.rotate(a)
			}
			if err == ErrUnavailable {
				retryAt = s.now().Add(time.Minute)
			} else {
				retryAt = time.Time{}
			}
			if err == ErrStorage {
				fatal = err
			}
		}
	}
}

// OAuth refreshes ten minutes before expiry, or halfway through a shorter
// lifetime. Unknown refresh expiry still gets an idle keep-alive each day.
func dueAt(a Account) time.Time {
	if a.Auth != provider.LoopbackOAuth {
		if a.AccessExpires != nil {
			return *a.AccessExpires
		}
		return time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	due := a.LastSuccess.Add(24 * time.Hour)
	for _, expiry := range []*time.Time{a.AccessExpires, a.RefreshExpires} {
		if expiry == nil {
			continue
		}
		lead := 10 * time.Minute
		if life := expiry.Sub(a.LastSuccess); life > 0 && life/2 < lead {
			lead = life / 2
		}
		next := expiry.Add(-lead)
		if next.Before(due) {
			due = next
		}
	}
	return due
}
func (s *Store) rotate(a Account) error {
	status := a.Status
	var rotated Rotated
	marked := false
	var metadataErr error
	err := s.vault.UpdateTokens(s.ctx, key(a), func(tokens secrets.Tokens) (secrets.Tokens, error) {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()
		a.Status = NeedsSignIn
		metadataErr = s.save(ctx, a)
		if metadataErr != nil {
			return secrets.Tokens{}, metadataErr
		}
		marked = true
		if s.refresh == nil || a.Auth != provider.LoopbackOAuth || !tokens.Valid(true) || (a.RefreshExpires != nil && !a.RefreshExpires.After(s.now())) {
			return secrets.Tokens{}, ErrInvalid
		}
		var err error
		rotated, err = s.refresh(ctx, a, tokens.Refresh())
		if err != nil || !validRotation(a, rotated, s.now()) {
			return secrets.Tokens{}, ErrInvalid
		}
		return rotated.Tokens, nil
	})
	if metadataErr != nil {
		return metadataErr
	}
	if err != nil {
		if !marked && s.ctx.Err() != nil {
			return ErrClosed
		}
		if !marked && err != secrets.ErrNotFound && err != secrets.ErrInvalid {
			return ErrUnavailable
		}
		if !marked {
			a.Status = NeedsSignIn
			if err := s.save(s.ctx, a); err != nil {
				return err
			}
		}
		return failure(NeedsSignIn)
	}
	// The secret-store write has completed (including file and directory sync)
	// before either the metadata becomes usable or Credential can return.
	return s.finish(s.ctx, a, rotated, status)
}
