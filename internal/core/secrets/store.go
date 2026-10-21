// SPDX-License-Identifier: Apache-2.0

// Package secrets owns credential storage and the masking boundary for native
// text. Core supplies an OS keychain Backend when available; OpenFiles is the
// explicit fallback for a host without a keychain. Store owns its backend.
package secrets

import (
	"errors"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/kit/secret"
)

var (
	ErrInvalid  = errors.New("invalid_credential")
	ErrNotFound = errors.New("credential_not_found")
	ErrStorage  = errors.New("credential_storage_failed")
	ErrInTurn   = errors.New("credential_change_during_turn")
	ErrClosed   = errors.New("credential_store_closed")
)

const maxCredentialBytes = 64 << 10

// Key binds a credential to its person, destination service and field. It is
// not an authorization grant; Core must authorize the destination before use.
type Key struct{ Person, Service, Name string }

func (k Key) valid() bool {
	for _, part := range []string{k.Person, k.Service, k.Name} {
		if part == "" || len(part) > 1024 || !utf8.ValidString(part) {
			return false
		}
		for _, c := range part {
			if unicode.IsControl(c) {
				return false
			}
		}
	}
	return true
}

// Backend is the trusted OS keychain seam. Read transfers ownership of its
// bytes to Store; Write consumes its input synchronously without retaining it.
// Only ErrNotFound survives the boundary; other errors are sanitized. A locked
// or failing keychain must not cause a silent fallback to file storage.
type Backend interface {
	Read(Key) ([]byte, error)
	Write(Key, []byte) error
	Delete(Key) error
	Close() error
}

type Store struct {
	mu      sync.Mutex
	backend Backend
	turns   int
	closed  bool
}

func NewStore(backend Backend) (*Store, error) {
	if backend == nil {
		return nil, ErrInvalid
	}
	return &Store{backend: backend}, nil
}

func (s *Store) Get(key Key) (secret.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return secret.Secret{}, ErrClosed
	}
	if !key.valid() {
		return secret.Secret{}, ErrInvalid
	}
	value, err := s.backend.Read(key)
	defer clear(value)
	if err != nil {
		return secret.Secret{}, storageError(err)
	}
	if len(value) == 0 || len(value) > maxCredentialBytes {
		return secret.Secret{}, ErrInvalid
	}
	return secret.New(string(value)), nil
}

// Put admits bytes from an account, plugin or channel credential form. Pasted
// conversation text belongs to Mask and must never call Put.
func (s *Store) Put(key Key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.canChange(key); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > maxCredentialBytes {
		return ErrInvalid
	}
	copy := append([]byte(nil), value...)
	defer clear(copy)
	return storageError(s.backend.Write(key, copy))
}

func (s *Store) Delete(key Key) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.canChange(key); err != nil {
		return err
	}
	return storageError(s.backend.Delete(key))
}

func (s *Store) canChange(key Key) error {
	if s.closed {
		return ErrClosed
	}
	if !key.valid() {
		return ErrInvalid
	}
	if s.turns != 0 {
		return ErrInTurn
	}
	return nil
}

// BeginTurn pins credentials until the returned, idempotent release is called.
// Overlapping turns all have to finish before rotation or deletion can proceed.
func (s *Store) BeginTurn() func() {
	s.mu.Lock()
	s.turns++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.turns--
			s.mu.Unlock()
		})
	}
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return storageError(s.backend.Close())
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return ErrStorage
}
