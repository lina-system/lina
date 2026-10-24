// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/thisisjun786/lina/kit/secret"
)

// Blob is an opaque byte credential for a component that must read it back,
// such as a messenger session. Only the secret store holds its plaintext, and
// ordinary serialization always redacts it. Bytes is the reveal boundary: the
// owning component may call it only for the destination the credential was
// issued for, never for a model request, a log or a client.
type Blob struct{ value func() []byte }

// NewBlob copies value into a Blob.
func NewBlob(value []byte) Blob {
	if value == nil {
		return Blob{}
	}
	copied := append([]byte(nil), value...)
	return Blob{value: func() []byte { return copied }}
}

func (Blob) String() string               { return secret.Placeholder }
func (Blob) GoString() string             { return secret.Placeholder }
func (Blob) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, secret.Placeholder) }
func (Blob) MarshalText() ([]byte, error) { return []byte(secret.Placeholder), nil }
func (Blob) MarshalJSON() ([]byte, error) { return json.Marshal(secret.Placeholder) }
func (Blob) LogValue() slog.Value         { return slog.StringValue(secret.Placeholder) }

// Ready reports whether the blob holds bytes.
func (b Blob) Ready() bool { return b.value != nil && len(b.value()) > 0 }

// Bytes returns a copy of the stored bytes.
func (b Blob) Bytes() []byte {
	if b.value == nil {
		return nil
	}
	return append([]byte(nil), b.value()...)
}

// GetBlob reads one opaque credential. A missing key returns ErrNotFound.
func (s *Store) GetBlob(key Key) (Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Blob{}, ErrClosed
	}
	if !key.valid() {
		return Blob{}, ErrInvalid
	}
	value, err := s.backend.Read(key)
	defer clear(value)
	if err != nil {
		return Blob{}, storageError(err)
	}
	if len(value) == 0 || len(value) > maxCredentialBytes {
		return Blob{}, ErrInvalid
	}
	return NewBlob(value), nil
}

// PutBlob writes one opaque credential. Like Put, it is refused while a turn
// pins credentials.
func (s *Store) PutBlob(key Key, value []byte) error {
	return s.Put(key, value)
}
