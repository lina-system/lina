// SPDX-License-Identifier: Apache-2.0
package responses

import (
	"context"
	"encoding/json"
	"errors"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/thisisjun786/lina/kit/provider"
)

type wireFailure struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

func byCode(code string) (provider.FailureKind, bool) {
	switch code {
	case "invalid_api_key", "invalid_token", "token_expired", "authentication_failed", "refresh_failed":
		return provider.NeedsSignIn, true
	case "not_eligible", "permission_denied", "insufficient_permissions", "model_not_found":
		return provider.NotEligible, true
	case "usage_limit_reached", "usage_limit_exceeded", "insufficient_quota", "rate_limit_exceeded":
		return provider.UsageCapped, true
	case "server_error", "internal_server_error", "service_unavailable", "overloaded":
		return provider.Unavailable, true
	case "unsupported_parameter", "unsupported_value", "invalid_request", "invalid_request_error", "unsupported_capability":
		return provider.NotAccepted, true
	}
	return "", false
}
func classify(w wireFailure, status int, id string) *provider.Failure {
	kind, ok := byCode(w.Code)
	if !ok {
		switch w.Type {
		case "authentication_error":
			kind = provider.NeedsSignIn
		case "permission_error", "not_eligible":
			kind = provider.NotEligible
		case "rate_limit_error", "insufficient_quota":
			kind = provider.UsageCapped
		case "server_error", "api_error", "overloaded_error":
			kind = provider.Unavailable
		case "invalid_request_error":
			kind = provider.NotAccepted
		default:
			switch {
			case status == 401:
				kind = provider.NeedsSignIn
			case status == 403:
				kind = provider.NotEligible
			case status == 429:
				kind = provider.UsageCapped
			case status == 0 || status == 408 || status >= 500:
				kind = provider.Unavailable
			default:
				kind = provider.NotAccepted
			}
		}
	}
	return &provider.Failure{Kind: kind, Code: w.Code, Type: w.Type, Status: status, RequestID: id}
}
func classifyError(ctx context.Context, err error, id string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var api *openai.Error
	if errors.As(err, &api) {
		return classify(wireFailure{Code: api.Code, Type: api.Type}, api.StatusCode, id)
	}
	var stream *ssestream.StreamError
	if errors.As(err, &stream) {
		var payload struct {
			wireFailure
			Error *wireFailure `json:"error"`
		}
		if json.Unmarshal(stream.Event.Data, &payload) == nil {
			if payload.Error != nil {
				return classify(*payload.Error, 0, id)
			}
			return classify(payload.wireFailure, 0, id)
		}
	}
	var failure *provider.Failure
	if errors.As(err, &failure) {
		return failure
	}
	return classify(wireFailure{}, 0, id)
}
