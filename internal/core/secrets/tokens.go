// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

// Tokens is an opaque, atomically replaced account credential record. Only the
// secret store encodes its plaintext; ordinary serialization is always redacted.
type Tokens struct{ value func() tokenRecord }
type tokenRecord struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
}

func NewTokens(access, refresh string) Tokens {
	return Tokens{value: func() tokenRecord { return tokenRecord{access, refresh} }}
}
func (Tokens) String() string               { return secret.Placeholder }
func (Tokens) GoString() string             { return secret.Placeholder }
func (Tokens) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, secret.Placeholder) }
func (Tokens) MarshalJSON() ([]byte, error) { return json.Marshal(secret.Placeholder) }
func (Tokens) MarshalText() ([]byte, error) { return []byte(secret.Placeholder), nil }
func (Tokens) LogValue() slog.Value         { return slog.StringValue(secret.Placeholder) }
func (t Tokens) Valid(refresh bool) bool {
	if t.value == nil {
		return false
	}
	r := t.value()
	return r.Access != "" && (!refresh || r.Refresh != "") && !strings.ContainsAny(r.Access+r.Refresh, "\x00\r\n") && len(r.Access)+len(r.Refresh) < maxCredentialBytes/2
}

// Access supplies the opaque bearer to a destination-bound service adapter.
func (t Tokens) Access() secret.Secret {
	if t.value == nil {
		return secret.Secret{}
	}
	return secret.New(t.value().Access)
}
func (t Tokens) Refresh() secret.Secret {
	if t.value == nil {
		return secret.Secret{}
	}
	return secret.New(t.value().Refresh)
}

// Credential supplies the opaque bearer only to the account's provider adapter.
func (t Tokens) Credential(id provider.AccountID, kind provider.AuthKind) provider.Credential {
	if t.value == nil {
		return provider.Credential{}
	}
	return provider.NewCredential(id, kind, t.value().Access)
}
func (s *Store) GetTokens(key Key) (Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Tokens{}, ErrClosed
	}
	if !key.valid() {
		return Tokens{}, ErrInvalid
	}
	return s.readTokens(key)
}
func (s *Store) readTokens(key Key) (Tokens, error) {
	raw, err := s.backend.Read(key)
	defer clear(raw)
	if err != nil {
		return Tokens{}, storageError(err)
	}
	if len(raw) > maxCredentialBytes {
		return Tokens{}, ErrInvalid
	}
	var record tokenRecord
	if json.Unmarshal(raw, &record) != nil {
		return Tokens{}, ErrInvalid
	}
	t := NewTokens(record.Access, record.Refresh)
	if !t.Valid(false) {
		return Tokens{}, ErrInvalid
	}
	return t, nil
}
func (s *Store) PutTokens(key Key, t Tokens) error {
	if !t.Valid(false) {
		return ErrInvalid
	}
	raw, _ := json.Marshal(t.value())
	defer clear(raw)
	return s.Put(key, raw)
}

// UpdateTokens reserves this account while fn exchanges a refresh token. It
// waits for existing turns and prevents new turns until the durable replacement
// completes. Other accounts may refresh concurrently. fn must honor ctx and
// must not call Store. Backend and callback diagnostics never cross this seam.
func (s *Store) UpdateTokens(ctx context.Context, key Key, fn func(Tokens) (Tokens, error)) error {
	if !key.valid() || fn == nil {
		return ErrInvalid
	}
	release, err := s.reserveTokens(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	current, err := s.readTokens(key)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	next, err := fn(current)
	if err != nil {
		return ErrUpdate
	}
	return s.writeTokens(key, next)
}

// ReplaceTokens reserves a sign-in replacement without requiring old tokens.
// before records intent and after commits metadata; both run outside the mutex
// but inside turn exclusion. Callbacks must not call Store. Their errors are
// sanitized, just like exchange errors. Cancellation before admission changes
// nothing; after admission the replacement must finish or fail closed.
func (s *Store) ReplaceTokens(ctx context.Context, key Key, next Tokens, before, after func() error) error {
	if !key.valid() || !next.Valid(false) || before == nil || after == nil {
		return ErrInvalid
	}
	release, err := s.reserveTokens(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	if before() != nil {
		return ErrUpdate
	}
	if err := s.writeTokens(key, next); err != nil {
		return err
	}
	if after() != nil {
		return ErrUpdate
	}
	return nil
}

// DeleteTokens waits for existing turns and keeps new turns excluded until
// before has recorded the account's unusable state and deletion has completed.
// before runs outside the mutex and must not call Store. Cancellation before
// reservation changes nothing; after reservation deletion finishes or fails
// closed. Missing credentials make repeated sign-out idempotent.
func (s *Store) DeleteTokens(ctx context.Context, key Key, before func() error) error {
	if !key.valid() || before == nil {
		return ErrInvalid
	}
	release, err := s.reserveTokens(ctx, key)
	if err != nil {
		return err
	}
	defer release()
	if before() != nil {
		return ErrUpdate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	err = storageError(s.backend.Delete(key))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (s *Store) reserveTokens(ctx context.Context, key Key) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.closed && (s.turns != 0 || s.updating[key]) {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			return nil, ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	if s.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.updating[key] = true
	return func() { s.mu.Lock(); delete(s.updating, key); s.signal(); s.mu.Unlock() }, nil
}

func (s *Store) writeTokens(key Key, next Tokens) error {
	if !next.Valid(false) {
		return ErrInvalid
	}
	raw, _ := json.Marshal(next.value())
	defer clear(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	// Never discard a successfully exchanged rotating token on cancellation.
	return storageError(s.backend.Write(key, raw))
}
func (s *Store) signal() { close(s.changed); s.changed = make(chan struct{}) }
