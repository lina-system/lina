// SPDX-License-Identifier: Apache-2.0
package publication

import (
	"context"
	"encoding/base64"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccessTokenBoundary(t *testing.T) {
	now := time.Unix(1800000000, 0)
	oauth := OAuth{Issuer: "https://identity.example", Audience: "lina-a2a", IntrospectionURL: "https://identity.example/introspect", ClientID: "core", Scopes: []string{"tasks"}}
	good := `{"active":true,"iss":"https://identity.example","aud":["lina-a2a"],"exp":1800000100,"sub":"caller","client_id":"client","scope":"tasks","token_type":"Bearer"}`
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"access", good, true},
		{"revoked", strings.Replace(good, `"active":true`, `"active":false`, 1), false},
		{"expired", strings.Replace(good, "1800000100", "1799999999", 1), false},
		{"issuer", strings.Replace(good, "identity.example", "evil.example", 1), false},
		{"audience", strings.Replace(good, "lina-a2a", "model-login", 1), false},
		{"scope", strings.Replace(good, `"scope":"tasks"`, `"scope":"profile"`, 1), false},
		{"id-token", strings.Replace(good, `"token_type":"Bearer"`, `"token_type":"id_token"`, 1), false},
		{"client", strings.Replace(good, `"client_id":"client"`, `"client_id":""`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Authenticator{OAuth: oauth, Now: func() time.Time { return now }, Secret: func() (secret.Secret, error) {
				return secret.New(base64.StdEncoding.EncodeToString([]byte("core:fixture-secret"))), nil
			}, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != oauth.IntrospectionURL || r.Method != "POST" {
					t.Fatal("wrong issuer transport")
				}
				user, password, ok := r.BasicAuth()
				if !ok || user != "core" || password != "fixture-secret" {
					t.Fatal("missing introspection authentication")
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("token") != "synthetic-access" || r.Form.Get("token_type_hint") != "access_token" {
					t.Fatal("wrong introspection request")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			p, err := a.Authenticate(context.Background(), "Bearer synthetic-access")
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v err=%v", err == nil, err)
			}
			if tc.ok && (p.Subject != "caller" || p.Client != "client" || p.Expires != 1800000100) {
				t.Fatalf("principal=%+v", p)
			}
		})
	}
}

func TestIntrospectionRefusesRedirects(t *testing.T) {
	calls := 0
	a := Authenticator{OAuth: OAuth{Issuer: "https://identity.example", Audience: "lina", IntrospectionURL: "https://identity.example/introspect", ClientID: "core"}, Secret: func() (secret.Secret, error) {
		return secret.New(base64.StdEncoding.EncodeToString([]byte("core:secret"))), nil
	}, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://other.example/token"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if _, err := a.Authenticate(t.Context(), "Bearer token"); err == nil || calls != 1 {
		t.Fatalf("redirect accepted or followed: %d %v", calls, err)
	}
}
