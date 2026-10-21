// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
)

var (
	ErrFenced = errors.New("activity admission is fenced")
	ErrBusy   = errors.New("restart requires known idle activity")
	ErrToken  = errors.New("invalid or expired fence token")
)

// Token belongs to one Prepare on one registry; its zero value grants nothing.
type Token struct{ attempt *attempt }
type attempt struct{ ctx context.Context }

type Fence interface {
	Prepare(context.Context) (Token, IdleReport)
	Commit(Token) error
	Cancel(Token)
}

// Prepare closes admission before observing activity. Existing leases remain
// valid until their owners finish; a busy token may be kept while draining.
// Additional activity sources must use this registry for admission too, so
// their snapshot cannot race an unregistered new effect past the fence.
func (r *ActivityRegistry) Prepare(ctx context.Context) (Token, IdleReport) {
	if r == nil || r.active == nil {
		return Token{}, IdleReport{State: Unknown, Err: ErrToken}
	}
	r.mu.Lock()
	if r.prepared != nil {
		r.mu.Unlock()
		return Token{}, IdleReport{State: Unknown, Err: ErrFenced}
	}
	token := Token{attempt: &attempt{ctx: ctx}}
	r.prepared = token.attempt
	r.mu.Unlock()
	return token, r.Idle(ctx)
}

// Commit rechecks idle while admission stays closed. Cancellation and Unknown
// never force a restart. A successful fence stays closed for process shutdown.
func (r *ActivityRegistry) Commit(token Token) error {
	if r == nil || token.attempt == nil {
		return ErrToken
	}
	r.mu.Lock()
	valid := r.prepared == token.attempt && !r.committed
	r.mu.Unlock()
	if !valid {
		return ErrToken
	}
	report := r.Idle(token.attempt.ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prepared != token.attempt || r.committed {
		return ErrToken
	}
	if report.Busy() {
		return errors.Join(ErrBusy, report.Err)
	}
	r.committed = true
	return nil
}

// Cancel reopens only the matching uncommitted fence. Stale and foreign tokens
// cannot cancel a newer attempt or undo a committed shutdown.
func (r *ActivityRegistry) Cancel(token Token) {
	if r == nil || token.attempt == nil {
		return
	}
	r.mu.Lock()
	if r.prepared == token.attempt && !r.committed {
		r.prepared = nil
	}
	r.mu.Unlock()
}
