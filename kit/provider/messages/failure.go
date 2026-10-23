// SPDX-License-Identifier: Apache-2.0
package messages

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/thisisjun786/lina/kit/provider"
)

type wireFailure struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func byType(typ string) (provider.FailureKind, bool) {
	switch typ {
	case "authentication_error", "invalid_api_key", "permission_denied":
		return provider.NeedsSignIn, true
	case "permission_error", "not_found_error", "not_eligible":
		return provider.NotEligible, true
	case "rate_limit_error", "billing_error", "insufficient_quota":
		return provider.UsageCapped, true
	case "overloaded_error", "api_error", "server_error", "timeout_error":
		return provider.Unavailable, true
	case "invalid_request_error", "request_too_large", "unsupported_parameter":
		return provider.NotAccepted, true
	}
	return "", false
}

func classify(w wireFailure, status int, id string) *provider.Failure {
	kind, ok := byType(w.Type)
	if !ok {
		switch {
		case status == http.StatusUnauthorized:
			kind = provider.NeedsSignIn
		case status == http.StatusForbidden, status == http.StatusNotFound:
			kind = provider.NotEligible
		case status == http.StatusRequestEntityTooLarge:
			kind = provider.NotAccepted
		case status == http.StatusTooManyRequests:
			kind = provider.UsageCapped
		case status == 0 || status == http.StatusRequestTimeout || status >= 500:
			kind = provider.Unavailable
		default:
			kind = provider.NotAccepted
		}
	}
	return &provider.Failure{Kind: kind, Type: w.Type, Status: status, RequestID: id}
}

func classifyError(ctx context.Context, err error, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *provider.Failure
	if errors.As(err, &failure) {
		return failure
	}
	return classify(wireFailure{}, 0, id)
}

func responseFailure(response *http.Response) error {
	var payload struct {
		Error wireFailure `json:"error"`
	}
	// Retain only machine identifiers, never the provider's message or body.
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	return classify(payload.Error, response.StatusCode, response.Header.Get("request-id"))
}
