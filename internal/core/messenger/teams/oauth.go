// SPDX-License-Identifier: Apache-2.0
package teams

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

var scopes = []string{"User.Read", "User.ReadBasic.All", "Chat.Read", "ChatMember.Read", "Team.ReadBasic.All", "Channel.ReadBasic.All", "ChannelMessage.Read.All", "ChannelMember.Read.All", "offline_access"}

type attempt struct {
	Connection                Connection
	State, Verifier, Redirect string
	Expires                   time.Time
}

func random() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Service) Begin(ctx context.Context, id, session string, port int) (string, error) {
	if session == "" || port < 1 || port > 65535 {
		return "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrUnavailable
	}
	c, err := s.connection(ctx, id)
	if err != nil {
		return "", err
	}
	for key, a := range s.attempts {
		if time.Now().After(a.Expires) {
			delete(s.attempts, key)
		}
	}
	if len(s.attempts) >= 64 {
		return "", ErrUnavailable
	}
	a := attempt{Connection: c, State: random(), Verifier: random(), Redirect: "http://localhost:" + strconv.Itoa(port) + "/auth/callback", Expires: time.Now().Add(10 * time.Minute)}
	hash := sha256.Sum256([]byte(a.Verifier))
	q := url.Values{"client_id": {c.ClientID}, "response_type": {"code"}, "redirect_uri": {a.Redirect}, "response_mode": {"query"}, "scope": {strings.Join(scopes, " ")}, "state": {a.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	s.attempts[session] = a
	return login + c.Tenant + "/oauth2/v2.0/authorize?" + q.Encode(), nil
}
func (s *Service) Cancel(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, session)
}
func (s *Service) CompleteURL(ctx context.Context, session, target string) error {
	s.mu.Lock()
	a, ok := s.attempts[session]
	s.mu.Unlock()
	u, err := url.Parse(target)
	if !ok || err != nil || (u.Scheme+"://"+u.Host+u.Path != a.Redirect && u.Scheme+"://"+u.Host+u.Path != strings.Replace(a.Redirect, "localhost:", "127.0.0.1:", 1)) || u.User != nil || u.Fragment != "" {
		s.Cancel(session)
		return ErrSignIn
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		s.Cancel(session)
		return ErrSignIn
	}
	for _, v := range q {
		if len(v) != 1 {
			s.Cancel(session)
			return ErrSignIn
		}
	}
	return s.Complete(ctx, session, q.Get("code"), q.Get("state"), q.Get("iss"), q.Get("error"))
}
func (s *Service) Complete(ctx context.Context, session, code, state, issuer, oauthError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[session]
	delete(s.attempts, session)
	if !ok || s.closed || time.Now().After(a.Expires) || state != a.State || issuer != "" && issuer != login+a.Connection.Tenant+"/v2.0" {
		return ErrSignIn
	}
	c, err := s.connection(ctx, a.Connection.ID)
	if err != nil || c.Tenant != a.Connection.Tenant || c.ClientID != a.Connection.ClientID {
		return ErrSignIn
	}
	s.stop(c.ID)
	fail := func(err error) error {
		c.Status = "needs_sign_in"
		c.Reason = ErrSignIn.Error()
		if err == ErrUnsupported {
			c.Status = "unsupported"
			c.Reason = err.Error()
		}
		if saveErr := s.save(ctx, c); saveErr != nil {
			return saveErr
		}
		if c.Account != "" {
			if saveErr := s.store.MarkFailure(ctx, s.person, "teams", c.Account, c.Reason); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if oauthError != "" {
		if oauthError == "access_denied" || oauthError == "consent_required" || oauthError == "unauthorized_client" {
			return fail(ErrUnsupported)
		}
		return fail(ErrSignIn)
	}
	if code == "" || len(code) > 8192 {
		return fail(ErrSignIn)
	}
	tokens, expiry, err := s.exchange(ctx, c, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {a.Verifier}, "redirect_uri": {a.Redirect}})
	if err != nil {
		return fail(err)
	}
	adapter := NewAdapter(Config{Tenant: c.Tenant, Transport: s.transport, Token: func(context.Context, bool) (secret.Secret, error) { return tokens.Access(), nil }})
	var me struct{ ID string }
	if err = adapter.get(ctx, graph+"/me?$select=id", &me); err != nil {
		return fail(err)
	}
	if me.ID == "" || strings.ContainsAny(me.ID, ":/\\") {
		return fail(ErrInvalid)
	}
	account := c.Tenant + ":" + me.ID
	if c.Account != "" && c.Account != account {
		return fail(ErrSignIn)
	}
	rows, err := s.Connections(ctx)
	if err != nil {
		return err
	}
	for _, other := range rows {
		if other.ID != c.ID && other.Account == account && other.Status != "disconnected" {
			return fail(ErrInvalid)
		}
	}
	c.Account = account
	c.Status = "connected"
	c.Reason = ""
	c.Expires = expiry
	err = s.vault.ReplaceTokens(ctx, s.key(c), tokens, func() error { pending := c; pending.Status = "needs_sign_in"; return s.save(ctx, pending) }, func() error {
		if err := s.store.Connect(ctx, s.source(c)); err != nil {
			return err
		}
		return s.save(ctx, c)
	})
	if err != nil {
		return err
	}
	s.start(c)
	return nil
}
func (s *Service) exchange(ctx context.Context, c Connection, form url.Values) (secrets.Tokens, time.Time, error) {
	form.Set("client_id", c.ClientID)
	form.Set("scope", strings.Join(scopes, " "))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, login+c.Tenant+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return secrets.Tokens{}, time.Time{}, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client(s.transport).Do(req)
	if err != nil {
		return secrets.Tokens{}, time.Time{}, ErrUnavailable
	}
	body, err := readBody(resp)
	defer clear(body)
	if err != nil {
		return secrets.Tokens{}, time.Time{}, err
	}
	var wire struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
		Scope   string `json:"scope"`
		Error   string `json:"error"`
		Codes   []int  `json:"error_codes"`
	}
	if json.Unmarshal(body, &wire) != nil {
		return secrets.Tokens{}, time.Time{}, ErrInvalid
	}
	if resp.StatusCode != 200 {
		for _, n := range wire.Codes {
			if n == 65001 || n == 65004 || n == 700016 || n == 7000112 || n == 53003 {
				return secrets.Tokens{}, time.Time{}, ErrUnsupported
			}
		}
		if wire.Error == "consent_required" || wire.Error == "unauthorized_client" || wire.Error == "access_denied" {
			return secrets.Tokens{}, time.Time{}, ErrUnsupported
		}
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			return secrets.Tokens{}, time.Time{}, &RetryError{After: retryAfter(resp.Header, time.Now())}
		}
		return secrets.Tokens{}, time.Time{}, ErrSignIn
	}
	granted := strings.Fields(wire.Scope)
	for _, scope := range scopes {
		if scope != "offline_access" && !slices.Contains(granted, scope) {
			return secrets.Tokens{}, time.Time{}, ErrUnsupported
		}
	}
	if !strings.EqualFold(wire.Type, "Bearer") || wire.Expires < 1 || wire.Expires > 86400 {
		return secrets.Tokens{}, time.Time{}, ErrSignIn
	}
	if wire.Refresh == "" {
		wire.Refresh = form.Get("refresh_token")
	}
	tokens := secrets.NewTokens(wire.Access, wire.Refresh)
	if !tokens.Valid(true) {
		return secrets.Tokens{}, time.Time{}, ErrSignIn
	}
	return tokens, time.Now().Add(time.Duration(wire.Expires) * time.Second), nil
}
func (s *Service) token(ctx context.Context, id string, force bool) (secret.Secret, error) {
	c, err := s.connection(ctx, id)
	if err != nil {
		return secret.Secret{}, err
	}
	if c.Status != "connected" && c.Status != "failing" {
		return secret.Secret{}, ErrSignIn
	}
	if force || time.Until(c.Expires) < time.Minute {
		var failure error
		err = s.vault.UpdateTokens(ctx, s.key(c), func(old secrets.Tokens) (secrets.Tokens, error) {
			header := make(http.Header)
			if err := secret.InjectHeader(header, "X-Refresh", "", old.Refresh()); err != nil {
				return secrets.Tokens{}, ErrSignIn
			}
			form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {header.Get("X-Refresh")}}
			header.Del("X-Refresh")
			next, expiry, err := s.exchange(ctx, c, form)
			failure = err
			if err != nil {
				return secrets.Tokens{}, err
			}
			c.Expires = expiry
			return next, nil
		})
		if failure != nil {
			return secret.Secret{}, failure
		}
		if err != nil {
			return secret.Secret{}, ErrSignIn
		}
		if err = s.save(ctx, c); err != nil {
			return secret.Secret{}, err
		}
	}
	tokens, err := s.vault.GetTokens(s.key(c))
	if err != nil {
		return secret.Secret{}, ErrSignIn
	}
	return tokens.Access(), nil
}
