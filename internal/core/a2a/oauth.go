// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type oauthAttempt struct {
	Connection                Connection
	State, Verifier, Redirect string
	Expires                   time.Time
}

func (s *Service) validateOAuth(ctx context.Context, c Connection) error {
	o := c.OAuth
	if o == nil || o.ClientID == "" || len(o.Scopes) == 0 || origin(o.Issuer) == "" {
		return ErrInvalid
	}
	for _, u := range []string{o.Issuer, o.AuthorizationURL, o.TokenURL} {
		if origin(u) != origin(o.Issuer) || s.Network.Check(ctx, u, c.PrivateHost) != nil {
			return ErrDestination
		}
	}
	var scheme struct {
		OAuth *struct {
			Flows struct {
				Code *struct {
					AuthorizationURL string            `json:"authorizationUrl"`
					TokenURL         string            `json:"tokenUrl"`
					Scopes           map[string]string `json:"scopes"`
				} `json:"authorizationCode"`
			} `json:"flows"`
		} `json:"oauth2SecurityScheme"`
	}
	if json.Unmarshal(c.Card.SecuritySchemes[c.Scheme], &scheme) != nil || scheme.OAuth == nil || scheme.OAuth.Flows.Code == nil {
		return ErrInvalid
	}
	flow := scheme.OAuth.Flows.Code
	if flow.AuthorizationURL != o.AuthorizationURL || flow.TokenURL != o.TokenURL {
		return ErrInvalid
	}
	for _, scope := range o.Scopes {
		if _, ok := flow.Scopes[scope]; !ok {
			return ErrInvalid
		}
	}
	return nil
}

// BeginOAuth uses the existing client's loopback listener. Pending verifier and
// state are process-local, session-bound and discarded on disconnect/restart.
func (s *Service) BeginOAuth(ctx context.Context, person, connection, session string, port int) (string, error) {
	c, err := s.Connection(ctx, person, connection)
	if err != nil || !c.Enabled || session == "" || port < 1 || port > 65535 {
		return "", ErrScope
	}
	if err = s.validateOAuth(ctx, c); err != nil {
		return "", err
	}
	attempt := oauthAttempt{Connection: c, State: randomID() + randomID(), Verifier: randomID() + randomID(), Redirect: "http://127.0.0.1:" + strconv.Itoa(port) + "/auth/callback", Expires: time.Now().Add(10 * time.Minute)}
	digest := sha256.Sum256([]byte(attempt.Verifier))
	u, _ := url.Parse(c.OAuth.AuthorizationURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", c.OAuth.ClientID)
	q.Set("redirect_uri", attempt.Redirect)
	q.Set("scope", strings.Join(c.OAuth.Scopes, " "))
	q.Set("state", attempt.State)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(digest[:]))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	if s.attempts == nil {
		s.attempts = map[string]oauthAttempt{}
	}
	for id, a := range s.attempts {
		if time.Now().After(a.Expires) {
			delete(s.attempts, id)
		}
	}
	if len(s.attempts) >= 64 {
		return "", ErrInvalid
	}
	s.attempts[session] = attempt
	return u.String(), nil
}
func (s *Service) DisconnectOAuth(session string) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	delete(s.attempts, session)
}
func (s *Service) CompleteOAuth(ctx context.Context, session, code, state, issuer string) error {
	s.oauthMu.Lock()
	a, ok := s.attempts[session]
	delete(s.attempts, session)
	s.oauthMu.Unlock()
	if !ok || code == "" || state != a.State || issuer != a.Connection.OAuth.Issuer || time.Now().After(a.Expires) {
		return ErrSignIn
	}
	c, err := s.Connection(ctx, a.Connection.Person, a.Connection.ID)
	if err != nil || !c.Enabled || c.Revision != a.Connection.Revision {
		return ErrScope
	}
	if err = s.validateOAuth(ctx, c); err != nil {
		return err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {c.OAuth.ClientID}, "code": {code}, "redirect_uri": {a.Redirect}, "code_verifier": {a.Verifier}}
	req, err := http.NewRequestWithContext(ctx, "POST", c.OAuth.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrSignIn
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Network.client(origin(c.OAuth.Issuer), c.PrivateHost).Do(req)
	if err != nil {
		return ErrSignIn
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrSignIn
	}
	raw, err := readBounded(resp.Body, 32<<10)
	if err != nil {
		return ErrSignIn
	}
	defer clear(raw)
	var token struct {
		Access  string `json:"access_token"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
		Expires int    `json:"expires_in"`
	}
	if json.Unmarshal(raw, &token) != nil || !strings.EqualFold(token.Type, "Bearer") || token.Access == "" || token.Expires < 1 || strings.ContainsAny(token.Access, "\r\n") {
		return ErrSignIn
	}
	if token.Scope != "" {
		for _, scope := range c.OAuth.Scopes {
			if !slices.Contains(strings.Fields(token.Scope), scope) {
				return ErrSignIn
			}
		}
	}
	// Recheck after exchange before storing for an owner-revoked connection.
	current, err := s.Connection(ctx, c.Person, c.ID)
	if err != nil || !current.Enabled || current.Revision != c.Revision {
		return ErrScope
	}
	if err = s.Vault.Put(key(c), []byte(token.Access)); err != nil {
		return ErrSignIn
	}
	c.CredentialExpires = time.Now().Add(time.Duration(min(token.Expires, 86400)) * time.Second)
	return s.saveConnection(ctx, c)
}
func (s *Service) SetCredential(ctx context.Context, person, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.Connection(ctx, person, id)
	if err != nil || !c.Enabled || c.Scheme == "none" || c.OAuth != nil || len(value) == 0 || len(value) > 8192 || strings.ContainsAny(string(value), "\r\n") {
		return ErrScope
	}
	return s.Vault.Put(key(c), value)
}

func (s *Service) CompleteOAuthURL(ctx context.Context, session, callback string) error {
	s.oauthMu.Lock()
	a, ok := s.attempts[session]
	s.oauthMu.Unlock()
	u, err := url.Parse(callback)
	if !ok || err != nil || u.User != nil || u.Fragment != "" || u.Scheme+"://"+u.Host+u.Path != a.Redirect {
		s.DisconnectOAuth(session)
		return ErrSignIn
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		s.DisconnectOAuth(session)
		return ErrSignIn
	}
	for k, v := range q {
		if len(v) != 1 || (k != "code" && k != "state" && k != "iss") {
			s.DisconnectOAuth(session)
			return ErrSignIn
		}
	}
	return s.CompleteOAuth(ctx, session, q.Get("code"), q.Get("state"), q.Get("iss"))
}
