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
	"strings"
	"testing"
	"time"
)

type signInTransport func(*http.Request) (*http.Response, error)

func (f signInTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func wireResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCodeExchangeValidatesAndKeepsTokensInCore(t *testing.T) {
	key, jwks, claims := tokenFixture(t)
	now := time.Unix(1800000001, 0)
	for _, test := range []struct {
		name, scope, subject string
		status               int
		want                 error
	}{
		{"new account", chatGPTScopes, "subject-one", 200, nil},
		{"missing plan permission", "openid profile email", "subject-one", 200, ErrSignInScope},
		{"different returning identity", chatGPTScopes, "subject-two", 200, ErrSignInIdentity},
		{"code rejected", chatGPTScopes, "subject-one", 400, ErrSignInRestart},
		{"unavailable", chatGPTScopes, "subject-one", 503, ErrSignInUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, _ := newAuthorization("host-one", "oaiapp_test", "subject-one", 32123, now)
			claims["nonce"] = a.nonce
			claims["sub"] = test.subject
			wire := map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "id_token": signFixture(t, key, claims, "RS256"), "scope": test.scope, "token_type": "Bearer", "expires_in": 3600, "earliest_refresh_at": int64(1800003000)}
			data, _ := json.Marshal(wire)
			calls := 0
			transport := signInTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() == openAIIssuer+"/api/accounts/oauth/token" {
					if calls != 1 || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Fatal("invalid token request")
					}
					body, _ := io.ReadAll(r.Body)
					values, err := url.ParseQuery(string(body))
					if err != nil || len(values) != 6 || values.Get("client_id") != "oaiapp_test" || values.Get("code") != "synthetic-code" || values.Get("code_verifier") != a.verifier || values.Get("redirect_uri") != a.redirect || values.Get("grant_type") != "authorization_code" || values.Get("resource") != openAIResource {
						t.Fatal("code exchange changed authorization binding")
					}
					if test.status != 200 {
						return wireResponse(test.status, `{"error":"invalid_grant","error_description":"synthetic-access"}`), nil
					}
					return wireResponse(200, string(data)), nil
				}
				if r.URL.String() != openAIIssuer+"/.well-known/jwks.json" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
					t.Fatal("unexpected request destination or credential")
				}
				return wireResponse(200, string(jwks)), nil
			})
			result, err := exchangeAuthorization(t.Context(), transport, a, Relayed{Code: "synthetic-code", State: a.state}, now)
			if !errors.Is(err, test.want) {
				t.Fatalf("result: %v, want %v", err, test.want)
			}
			if calls > 2 || calls < 1 {
				t.Fatalf("retried exchange: %d requests", calls)
			}
			if err == nil {
				if result.identity.Subject != "subject-one" || result.clientID != "oaiapp_test" || result.accessExpires != now.Add(time.Hour) {
					t.Fatal("missing verified metadata")
				}
				for _, formatted := range []string{fmt.Sprintf("%v", result), fmt.Sprintf("%+v", result), fmt.Sprintf("%#v", result)} {
					if strings.Contains(formatted, "synthetic-access") || strings.Contains(formatted, "synthetic-refresh") {
						t.Fatal("token leaked in formatting")
					}
				}
			} else if strings.Contains(err.Error(), "synthetic-access") {
				t.Fatal("token endpoint diagnostic leaked")
			}
		})
	}
}

func TestExchangeRejectsCallbackBeforeNetworkAndNeverFollowsRedirect(t *testing.T) {
	now := time.Unix(1800000001, 0)
	a, _ := newAuthorization("host-one", "", "", 32123, now)
	calls := 0
	transport := signInTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		response := wireResponse(307, "")
		response.Header.Set("Location", "https://wrong.invalid/token")
		return response, nil
	})
	if _, err := exchangeAuthorization(context.Background(), transport, a, Relayed{Code: "synthetic-code", State: "wrong", ClientID: "oaiapp_new"}, now); !errors.Is(err, ErrSignInInvalid) || calls != 0 {
		t.Fatal("unvalidated callback reached network")
	}
	if _, err := exchangeAuthorization(t.Context(), transport, a, Relayed{Code: "synthetic-code", State: a.state, ClientID: "oaiapp_new"}, now); !errors.Is(err, ErrSignInUnavailable) || calls != 1 {
		t.Fatalf("redirect followed: requests=%d error=%v", calls, err)
	}
}
