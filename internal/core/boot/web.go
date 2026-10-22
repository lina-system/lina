// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) webKey() secrets.Key {
	return secrets.Key{Person: r.person, Service: "firecrawl", Name: "api_key"}
}
func (r *conversationRuntime) selectedWebProvider() (coreexec.WebProvider, error) {
	key, err := r.vault.Get(r.webKey())
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return coreexec.NewFirecrawl(key, r.webTransport), nil
}

// Web accepts credentials only from an authenticated settings form, never from
// conversation text. The response reports configuration, not key validity.
func (r *conversationRuntime) Web(ctx context.Context, s clientsrv.Session, p protocol.WebRequest) (protocol.WebResult, error) {
	result := protocol.WebResult{Provider: "firecrawl"}
	if err := r.authorize(s, nil); err != nil {
		return result, err
	}
	if s.Epoch != r.epoch || p.Operation != "status" && !s.CanDecide {
		return result, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	switch p.Operation {
	case "status", "clear":
		if p.Key != nil {
			return result, malformedManagement()
		}
	case "set":
		if p.Key == nil || len(*p.Key) < 1 || len(*p.Key) > 512 || strings.IndexFunc(*p.Key, func(c rune) bool { return c > 126 || unicode.IsSpace(c) || unicode.IsControl(c) }) >= 0 {
			return result, malformedManagement()
		}
	default:
		return result, malformedManagement()
	}
	var err error
	switch p.Operation {
	case "set":
		key := []byte(*p.Key)
		defer clear(key)
		err = r.vault.Put(r.webKey(), key)
	case "clear":
		err = r.vault.Delete(r.webKey())
		if errors.Is(err, secrets.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return result, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	_, err = r.vault.Get(r.webKey())
	if errors.Is(err, secrets.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return result, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	result.Configured = true
	return result, nil
}
