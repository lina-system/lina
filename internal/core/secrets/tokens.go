// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"context"
	"encoding/json"
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
	s.mu.Lock()
	for !s.closed && (s.turns != 0 || s.updating[key]) {
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	current, err := s.readTokens(key)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.updating[key] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.updating, key); s.signal(); s.mu.Unlock() }()
	next, err := fn(current)
	if err != nil {
		return ErrUpdate
	}
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
	// Even if the caller was canceled after a successful exchange, persist the
	// rotated token: cancellation must not discard the only usable refresh token.
	return storageError(s.backend.Write(key, raw))
}
func (s *Store) signal() { close(s.changed); s.changed = make(chan struct{}) }
