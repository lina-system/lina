// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

// ChatGPTRefresher supplies the fixed-issuer exchange to the account actor.
// The actor owns serialization and durable rotation; this callback retries
// nothing and never returns an issuer diagnostic or a plaintext credential.
func ChatGPTRefresher(leaser lease.Leaser, transport http.RoundTripper) Refresher {
	return func(ctx context.Context, a Account, refresh secret.Secret) (Rotated, error) {
		if leaser == nil || a.Provider != provider.ChatGPT || a.Auth != provider.LoopbackOAuth || a.ClientID == "" || a.ClientID == registrationClient {
			return Rotated{}, ErrSignInInvalid
		}
		activity, err := leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "ChatGPT credential refresh"})
		if err != nil {
			return Rotated{}, ErrSignInUnavailable
		}
		defer activity.Release()
		var result Rotated
		err = lease.Do(ctx, activity, func(ctx context.Context) error {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIIssuer+"/api/accounts/oauth/token", nil)
			if err != nil {
				return ErrSignInInvalid
			}
			// Use the approved HTTP secret-injection boundary in Core's account reveal
			// site. The issuer requires this credential in the form body; the temporary
			// header is removed before the request is handed to the HTTP transport.
			if err := secret.InjectHeader(request.Header, "X-Lina-Refresh", "", refresh); err != nil {
				return ErrSignInInvalid
			}
			form := url.Values{"grant_type": {"refresh_token"}, "client_id": {a.ClientID}, "resource": {openAIResource}, "refresh_token": {request.Header.Get("X-Lina-Refresh")}}
			request.Header.Del("X-Lina-Refresh")
			encoded := form.Encode()
			request.Body = io.NopCloser(strings.NewReader(encoded))
			request.ContentLength = int64(len(encoded))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return ErrSignInUnavailable
			}
			raw, err := readSignInResponse(response, 64<<10)
			if err != nil || response.StatusCode != http.StatusOK {
				return ErrSignInUnavailable
			}
			var tokens signInTokenWire
			if json.Unmarshal(raw, &tokens) != nil || tokens.Access == "" || tokens.Refresh == "" || !strings.EqualFold(tokens.Type, "Bearer") || tokens.Expires <= 0 || tokens.Expires > 86400 {
				return ErrSignInInvalid
			}
			scopes := slices.Clone(a.Scopes)
			if tokens.Scope != "" {
				scopes = strings.Fields(tokens.Scope)
			}
			if !slices.Contains(scopes, "chatgpt.tokens.use.direct") {
				return ErrSignInScope
			}
			now := time.Now()
			accessExpires, refreshExpires := now.Add(time.Duration(tokens.Expires)*time.Second), now.Add(30*24*time.Hour)
			result = Rotated{Tokens: secrets.NewTokens(tokens.Access, tokens.Refresh), AccessExpires: &accessExpires, RefreshExpires: &refreshExpires, Scopes: scopes}
			if !result.Tokens.Valid(true) {
				return ErrSignInInvalid
			}
			return nil
		})
		if err != nil {
			return Rotated{}, err
		}
		return result, nil
	}
}
