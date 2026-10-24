// SPDX-License-Identifier: Apache-2.0

package webex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// Redirect is what the client relayed from the sign-in browser: the code and
// the state the authorization server returned, or the error fields of a
// refused sign-in. Core validates these against its pending attempt.
type Redirect struct {
	Code, State, Iss string
	Error            string
	ErrorDescription string
	// URL is the redirected address itself, for a client that cannot receive a
	// loopback redirect and the person pasted the address instead.
	URL string
}

// Configure records the person's own Webex integration. The client id is
// public; the secret goes to the secret store and is not returned.
func (a *Adapter) Configure(ctx context.Context, clientID, clientSecret string) error {
	if clientID == "" || clientSecret == "" || strings.ContainsAny(clientID+clientSecret, "\r\n\x00") {
		return ErrMisconfigured
	}
	if err := a.vault.Put(ClientIDKey(a.person), []byte(clientID)); err != nil {
		return err
	}
	return a.vault.Put(ClientKey(a.person), []byte(clientSecret))
}

// Configured reports whether the person's integration is recorded.
func (a *Adapter) Configured() bool {
	if _, err := a.vault.Get(ClientIDKey(a.person)); err != nil {
		return false
	}
	_, err := a.vault.Get(ClientKey(a.person))
	return err == nil
}

// BeginSignIn builds the authorization URL for a loopback redirect on port. The
// PKCE verifier and the state stay in this process and are discarded on a
// completed or abandoned sign-in.
func (a *Adapter) BeginSignIn(ctx context.Context, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", ErrMisconfigured
	}
	clientID, err := a.vault.Get(ClientIDKey(a.person))
	if err != nil {
		return "", ErrMisconfigured
	}
	client, err := reveal(clientID)
	if err != nil {
		return "", err
	}
	verifier := randomID()
	state := randomID() + randomID()
	digest := sha256.Sum256([]byte(verifier))
	attempt := &attempt{
		Verifier: verifier,
		State:    state,
		Redirect: "http://127.0.0.1:" + strconv.Itoa(port) + redirectPath,
		Expires:  a.now().Add(10 * time.Minute),
	}
	a.mu.Lock()
	a.pending = attempt
	a.mu.Unlock()
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {client},
		"redirect_uri":          {attempt.Redirect},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(digest[:])},
		"code_challenge_method": {"S256"},
	}
	return a.base + "/v1/authorize?" + query.Encode(), nil
}

// CompleteSignIn exchanges the relayed code for tokens and records the Webex
// person they belong to.
func (a *Adapter) CompleteSignIn(ctx context.Context, redirect Redirect) (Account, error) {
	a.mu.Lock()
	pending := a.pending
	a.pending = nil
	a.mu.Unlock()
	if pending == nil || !a.now().Before(pending.Expires) {
		return Account{}, messenger.ErrNeedsSignIn
	}
	if redirect.URL != "" {
		parsed, err := url.Parse(redirect.URL)
		if err != nil || parsed.Scheme+"://"+parsed.Host+parsed.Path != pending.Redirect || parsed.User != nil || parsed.Fragment != "" {
			return Account{}, ErrMisconfigured
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return Account{}, ErrMisconfigured
		}
		for _, values := range query {
			if len(values) != 1 {
				return Account{}, ErrMisconfigured
			}
		}
		redirect = Redirect{Iss: query.Get("iss"), Code: query.Get("code"), State: query.Get("state"), Error: query.Get("error"), ErrorDescription: query.Get("error_description")}
	}
	if redirect.Error != "" || redirect.Code == "" || redirect.State != pending.State || redirect.Iss != "" && redirect.Iss != a.base {
		return Account{}, messenger.ErrNeedsSignIn
	}
	tokens, err := a.exchange(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {redirect.Code},
		"redirect_uri":  {pending.Redirect},
		"code_verifier": {pending.Verifier},
	})
	if err != nil {
		return Account{}, err
	}

	// Validate the new token's identity before replacing a connected account.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/v1/people/me", nil)
	if err != nil {
		return Account{}, ErrMisconfigured
	}
	if err := secret.InjectHeader(request.Header, "Authorization", "Bearer ", tokens.Access()); err != nil {
		return Account{}, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return Account{}, errors.New("webex profile unavailable")
	}
	defer response.Body.Close()
	var profile wirePerson
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&profile) != nil || profile.ID == "" {
		return Account{}, ErrMisconfigured
	}
	if a.expected != "" && profile.ID != a.expected {
		return Account{}, messenger.ErrInvalid
	}
	account := Account{ID: profile.ID, Name: profile.DisplayName}
	if len(profile.Emails) > 0 {
		account.Email = profile.Emails[0]
	}
	if err := a.vault.PutTokens(TokenKey(a.person), tokens); err != nil {
		return Account{}, err
	}
	a.mu.Lock()
	a.account, a.haveMe = account, true
	a.mu.Unlock()
	return account, nil
}

// AbandonSignIn discards a sign-in the person did not finish.
func (a *Adapter) AbandonSignIn() {
	a.mu.Lock()
	a.pending = nil
	a.mu.Unlock()
}

// Forget deletes the person's tokens and integration. Records the source
// already admitted stay.
func (a *Adapter) Forget() error {
	for _, key := range []secrets.Key{TokenKey(a.person), ClientKey(a.person), ClientIDKey(a.person)} {
		if err := a.vault.Delete(key); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return err
		}
	}
	return nil
}

// access returns a usable access token, refreshing shortly before the recorded
// expiry. A refresh the platform refuses deletes the dead credential and marks
// the source needs sign-in.
func (a *Adapter) access(ctx context.Context) (secret.Secret, error) {
	tokens, err := a.vault.GetTokens(TokenKey(a.person))
	if err != nil {
		return secret.Secret{}, messenger.ErrNeedsSignIn
	}
	if expires := tokens.Expires(); !expires.IsZero() && a.now().Add(refreshLead).Before(expires) {
		return tokens.Access(), nil
	}
	return a.refresh(ctx, false)
}

// refresh exchanges the refresh token. Serialized: one refresh replaces the
// stored tokens, and a concurrent caller reuses its result instead of spending
// the rotation twice.
func (a *Adapter) refresh(ctx context.Context, forced bool) (secret.Secret, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	tokens, err := a.vault.GetTokens(TokenKey(a.person))
	if err != nil {
		return secret.Secret{}, messenger.ErrNeedsSignIn
	}
	if !forced {
		if expires := tokens.Expires(); !expires.IsZero() && a.now().Add(refreshLead).Before(expires) {
			return tokens.Access(), nil
		}
	}
	refresh, err := reveal(tokens.Refresh())
	if err != nil {
		return secret.Secret{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}
	next, err := a.exchange(ctx, form)
	if err != nil {
		if errors.Is(err, errRefused) {
			// Webex refuses a revoked or expired refresh token with
			// invalid_grant. The source needs the person to sign in again.
			_ = a.vault.DeleteTokens(ctx, TokenKey(a.person), func() error { return nil })
			return secret.Secret{}, messenger.ErrNeedsSignIn
		}
		return secret.Secret{}, err
	}
	if err := a.vault.PutTokens(TokenKey(a.person), next); err != nil {
		return secret.Secret{}, err
	}
	return next.Access(), nil
}

// errRefused marks a token response the platform rejected, as opposed to a
// transport or shape failure that a later read may succeed at.
var errRefused = errors.New("webex_token_refused")

// exchange posts one token request and reads the response.
func (a *Adapter) exchange(ctx context.Context, form url.Values) (secrets.Tokens, error) {
	clientID, err := a.vault.Get(ClientIDKey(a.person))
	if err != nil {
		return secrets.Tokens{}, ErrMisconfigured
	}
	clientSecret, err := a.vault.Get(ClientKey(a.person))
	if err != nil {
		return secrets.Tokens{}, ErrMisconfigured
	}
	id, err := reveal(clientID)
	if err != nil {
		return secrets.Tokens{}, err
	}
	shared, err := reveal(clientSecret)
	if err != nil {
		return secrets.Tokens{}, err
	}
	form.Set("client_id", id)
	form.Set("client_secret", shared)
	encoded := form.Encode()
	// Credentials are sent only in this origin-bound token request.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/v1/access_token", strings.NewReader(encoded))
	if err != nil {
		return secrets.Tokens{}, ErrMisconfigured
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := a.client.Do(request)
	if err != nil {
		return secrets.Tokens{}, errors.New("webex token endpoint unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	defer clear(raw)
	if err != nil {
		return secrets.Tokens{}, err
	}
	if response.StatusCode != http.StatusOK {
		var refused wireError
		if json.Unmarshal(raw, &refused) == nil && refused.Error != "" {
			if refused.Error == "invalid_grant" || refused.Error == "invalid_client" || refused.Error == "unauthorized_client" {
				return secrets.Tokens{}, errRefused
			}
			return secrets.Tokens{}, errors.New("webex token rejected")
		}
		return secrets.Tokens{}, fmt.Errorf("webex token status %d", response.StatusCode)
	}
	var token wireToken
	if json.Unmarshal(raw, &token) != nil || token.Access == "" || token.Refresh == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*24*3600 || strings.ContainsAny(token.Access+token.Refresh, "\x00\r\n") {
		return secrets.Tokens{}, fmt.Errorf("webex token response shape")
	}
	expires := a.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return secrets.NewTokensUntil(token.Access, token.Refresh, expires), nil
}

// reveal reads one credential for a form body. kit/secret offers only header
// and environment injection as exits, so this uses the same temporary-header
// boundary Core's model account refresh uses; the caller overwrites the value
// in the form as soon as the request is built.
func reveal(value secret.Secret) (string, error) {
	header := http.Header{}
	if err := secret.InjectHeader(header, "X-Lina-Form", "", value); err != nil {
		return "", ErrMisconfigured
	}
	text := header.Get("X-Lina-Form")
	header.Del("X-Lina-Form")
	return text, nil
}

func randomID() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
