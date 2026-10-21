// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

var (
	ErrSignInScope       = errors.New("chatgpt_plan_permission_missing")
	ErrSignInRestart     = errors.New("sign_in_restart_required")
	ErrSignInUnavailable = errors.New("sign_in_unavailable")
)

type signInTokenWire struct {
	Access          string `json:"access_token"`
	Refresh         string `json:"refresh_token"`
	ID              string `json:"id_token"`
	Type            string `json:"token_type"`
	Scope           string `json:"scope"`
	Expires         int64  `json:"expires_in"`
	EarliestRefresh *int64 `json:"earliest_refresh_at,omitempty"`
}

func (signInTokenWire) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[sign-in credentials]") }

type authorizedTokens struct {
	identity      verifiedIdentity
	clientID      string
	tokens        signInTokenWire
	scopes        []string
	accessExpires time.Time
}

func (authorizedTokens) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[verified sign-in]") }

func exchangeAuthorization(ctx context.Context, transport http.RoundTripper, a authorization, in Relayed, now time.Time) (authorizedTokens, error) {
	callback, err := a.callback(in, now)
	if err != nil {
		return authorizedTokens{}, err
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	values := url.Values{"grant_type": {"authorization_code"}, "client_id": {callback.ClientID}, "code": {callback.Code}, "code_verifier": {a.verifier}, "redirect_uri": {a.redirect}, "resource": {openAIResource}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIIssuer+"/api/accounts/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return authorizedTokens{}, ErrSignInInvalid
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return authorizedTokens{}, ErrSignInUnavailable
	}
	data, err := readSignInResponse(response, 64<<10)
	if err != nil {
		return authorizedTokens{}, err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"error"`
		}
		if response.StatusCode == 400 && json.Unmarshal(data, &failure) == nil && failure.Code == "invalid_grant" {
			return authorizedTokens{}, ErrSignInRestart
		}
		return authorizedTokens{}, ErrSignInUnavailable
	}
	var tokens signInTokenWire
	if json.Unmarshal(data, &tokens) != nil || tokens.Access == "" || tokens.Refresh == "" || tokens.ID == "" || tokens.Type != "Bearer" || tokens.Expires <= 0 || tokens.Expires > 86400 {
		return authorizedTokens{}, ErrSignInInvalid
	}
	scopes := strings.Fields(tokens.Scope)
	if !slices.Contains(scopes, "chatgpt.tokens.use.direct") {
		return authorizedTokens{}, ErrSignInScope
	}
	keysRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, openAIIssuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return authorizedTokens{}, ErrSignInInvalid
	}
	keysResponse, err := client.Do(keysRequest)
	if err != nil {
		return authorizedTokens{}, ErrSignInUnavailable
	}
	keys, err := readSignInResponse(keysResponse, 128<<10)
	if err != nil || keysResponse.StatusCode != http.StatusOK {
		return authorizedTokens{}, ErrSignInUnavailable
	}
	identity, err := verifyIDToken(tokens.ID, keys, callback.ClientID, a.nonce, now)
	if err != nil {
		return authorizedTokens{}, err
	}
	if a.subject != "" && a.subject != identity.Subject {
		return authorizedTokens{}, ErrSignInIdentity
	}
	return authorizedTokens{identity: identity, clientID: callback.ClientID, tokens: tokens, scopes: scopes, accessExpires: now.Add(time.Duration(tokens.Expires) * time.Second)}, nil
}

func readSignInResponse(response *http.Response, limit int64) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrSignInUnavailable
	}
	return data, nil
}
