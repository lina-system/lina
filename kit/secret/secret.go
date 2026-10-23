// SPDX-License-Identifier: Apache-2.0

// Package secret keeps credentials opaque except at environment and HTTP
// header injection. It does not load credentials or authorize their use.
package secret

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

const Placeholder = "[REDACTED]"

var ErrInvalid = errors.New("invalid_secret_injection")

// Secret has no plaintext field: even reflection-based formatting of an
// unexported containing field cannot bypass Format and print the credential.
// The zero value is empty, but formats identically to a populated Secret.
type Secret struct{ value func() string }

func New(value string) Secret { return Secret{value: func() string { return value }} }

func (Secret) String() string               { return Placeholder }
func (Secret) GoString() string             { return Placeholder }
func (Secret) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, Placeholder) }
func (Secret) MarshalText() ([]byte, error) { return []byte(Placeholder), nil }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Placeholder + `"`), nil }
func (Secret) LogValue() slog.Value         { return slog.StringValue(Placeholder) }

// Ready reports whether a value is present. It reveals only that, never the
// value, so a caller can refuse a request that would carry an empty credential.
func (s Secret) Ready() bool { return s.value != nil && s.value() != "" }

// Environment is one of the two plaintext exit points. The caller must have
// authorized this credential for the destination process, outside the sandbox.
func Environment(name string, value Secret) (string, error) {
	if name == "" || strings.ContainsAny(name, "=\x00") || value.value == nil {
		return "", ErrInvalid
	}
	raw := value.value()
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", ErrInvalid
	}
	return name + "=" + raw, nil
}

// InjectHeader is the other plaintext exit point. The caller must bind the
// request to the server for which the credential was issued; redirects must
// not forward the header to a different server.
func InjectHeader(headers http.Header, name, prefix string, value Secret) error {
	if headers == nil || !headerName(name) || value.value == nil {
		return ErrInvalid
	}
	raw := value.value()
	if raw == "" || !headerValue(prefix+raw) {
		return ErrInvalid
	}
	headers.Set(name, prefix+raw)
	return nil
}

func headerName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func headerValue(value string) bool {
	for i := range len(value) {
		if value[i] == 127 || value[i] < 32 && value[i] != '\t' {
			return false
		}
	}
	return true
}
