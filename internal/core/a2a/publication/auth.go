// SPDX-License-Identifier: Apache-2.0
package publication

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type Principal struct {
	Issuer, Subject, Client, Policy string
	Expires                         int64
}

func (p Principal) Key() string { return digest([]string{p.Issuer, p.Subject, p.Client}) }

type Authenticator struct {
	OAuth     OAuth
	Transport http.RoundTripper
	Secret    func() (secret.Secret, error)
	Now       func() time.Time
}

// Introspection is issuer-bound, uncached and redirect-free. The endpoint must
// identify access tokens (Bearer); an ID-token introspection result is refused.
func (a Authenticator) Authenticate(ctx context.Context, header string) (Principal, error) {
	deny := func() (Principal, error) { return Principal{}, ErrAuthentication }
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > 8192 || strings.ContainsAny(token, " \r\n\t") {
		return deny()
	}
	issuer, e1 := httpsURL(a.OAuth.Issuer)
	endpoint, e2 := httpsURL(a.OAuth.IntrospectionURL)
	if e1 != nil || e2 != nil || issuer.Host != endpoint.Host || a.Secret == nil {
		return deny()
	}
	credential, err := a.Secret()
	if err != nil {
		return deny()
	}
	form := url.Values{"token": {token}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return deny()
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secret.InjectHeader(req.Header, "Authorization", "Basic ", credential) != nil {
		return deny()
	}
	client := &http.Client{Transport: a.Transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return deny()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return deny()
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxInput+1))
	if err != nil || len(raw) > MaxInput {
		return deny()
	}
	var claims struct {
		Active    bool
		Iss       string
		Aud       json.RawMessage
		Exp       int64
		Nbf       int64
		Sub       string
		Client    string `json:"client_id"`
		Scope     string
		TokenType string `json:"token_type"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return deny()
	}
	var audiences []string
	if json.Unmarshal(claims.Aud, &audiences) != nil {
		var audience string
		if json.Unmarshal(claims.Aud, &audience) != nil {
			return deny()
		}
		audiences = []string{audience}
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	if !claims.Active || claims.Iss != a.OAuth.Issuer || !slices.Contains(audiences, a.OAuth.Audience) || claims.Exp <= now.Unix() || claims.Nbf > now.Unix() || claims.Sub == "" || claims.Client == "" || !strings.EqualFold(claims.TokenType, "Bearer") {
		return deny()
	}
	scopes := strings.Fields(claims.Scope)
	for _, scope := range a.OAuth.Scopes {
		if !slices.Contains(scopes, scope) {
			return deny()
		}
	}
	return Principal{Issuer: claims.Iss, Subject: claims.Sub, Client: claims.Client, Expires: claims.Exp, Policy: digest(a.OAuth)}, nil
}
