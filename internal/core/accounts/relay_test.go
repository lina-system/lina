// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestAuthorizationUsesFreshPKCEAndIssuedRegistration(t *testing.T) {
	now := time.Unix(1800000000, 0)
	a, err := newAuthorization("host-one", "", "", 32123, now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(a.url)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/api/accounts/authorize" ||
		q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "LINA" || q.Get("ext_agent_host_id") != "host-one" ||
		q.Get("resource") != "https://api.openai.com/v1" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != pkceChallenge(a.verifier) ||
		q.Get("redirect_uri") != "http://127.0.0.1:32123/auth/callback" || q.Get("state") != a.state || q.Get("nonce") != a.nonce {
		t.Fatal("authorization parameters do not match the public-client contract")
	}
	b, err := newAuthorization("host-one", "oaiapp_saved", "subject-one", 32124, now)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(b.url)
	if u.Query().Get("client_id") != "oaiapp_saved" || u.Query().Has("agent_name_hint") || a.state == b.state || a.nonce == b.nonce || a.verifier == b.verifier {
		t.Fatal("returning registration changed or randomness reused")
	}
	for _, port := range []int{-1, 0, 65536} {
		if _, err := newAuthorization("host-one", "", "", port, now); !errors.Is(err, ErrSignInInvalid) {
			t.Fatalf("port %d: %v", port, err)
		}
	}
}

func TestRedirectValidatesBeforeExchange(t *testing.T) {
	now := time.Unix(1800000000, 0)
	a, _ := newAuthorization("host-one", "", "", 32123, now)
	good := Relayed{Code: "synthetic-code", State: a.state, Issuer: openAIIssuer, ClientID: "oaiapp_new"}
	for _, test := range []struct {
		name   string
		change func(*Relayed)
		want   error
	}{
		{"state", func(r *Relayed) { r.State = "other" }, ErrSignInInvalid},
		{"issuer", func(r *Relayed) { r.Issuer = "https://wrong.invalid" }, ErrSignInInvalid},
		{"missing client", func(r *Relayed) { r.ClientID = "" }, ErrSignInInvalid},
		{"registration placeholder", func(r *Relayed) { r.ClientID = "dynamic_agent_client" }, ErrSignInInvalid},
		{"missing code", func(r *Relayed) { r.Code = "" }, ErrSignInInvalid},
		{"denied", func(r *Relayed) { r.Code = ""; r.Error = "access_denied" }, ErrSignInDenied},
		{"code and error", func(r *Relayed) { r.Error = "access_denied" }, ErrSignInInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := good
			test.change(&r)
			if _, err := a.callback(r, now); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
	if _, err := a.callback(good, now.Add(11*time.Minute)); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("expired: %v", err)
	}
	if r, err := a.callback(good, now); err != nil || r.ClientID != good.ClientID {
		t.Fatalf("valid callback: %v", err)
	}
	// OAuth issuer identification is optional on this single fixed issuer route;
	// when present it must match. The ID token is always verified separately.
	good.Issuer = ""
	if _, err := a.callback(good, now); err != nil {
		t.Fatalf("documented callback without iss: %v", err)
	}
	a.clientID = "oaiapp_saved"
	if _, err := a.callback(good, now); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("replaced returning client: %v", err)
	}
	good.ClientID = ""
	if r, err := a.callback(good, now); err != nil || r.ClientID != a.clientID {
		t.Fatalf("saved client: %v", err)
	}
}

func TestPastedRedirectRequiresExactOriginPathAndUnambiguousFields(t *testing.T) {
	now := time.Unix(1800000000, 0)
	a, _ := newAuthorization("host-one", "", "", 32123, now)
	query := url.Values{"code": {"synthetic-code"}, "state": {a.state}, "client_id": {"oaiapp_new"}}.Encode()
	if got, err := a.callback(Relayed{URL: a.redirect + "?" + query}, now); err != nil || got.Code != "synthetic-code" {
		t.Fatalf("pasted: %v", err)
	}
	for _, raw := range []string{
		"http://localhost:32123/auth/callback?" + query,
		"http://127.0.0.1:32124/auth/callback?" + query,
		"http://127.0.0.1:32123/other?" + query,
		a.redirect + "?" + query + "&state=second",
		a.redirect + "?" + query + "#fragment",
		"http://user@127.0.0.1:32123/auth/callback?" + query,
		a.redirect + "?" + query + "&access_token=secret",
	} {
		if _, err := a.callback(Relayed{URL: raw}, now); !errors.Is(err, ErrSignInInvalid) {
			t.Fatalf("accepted ambiguous/foreign URL: %v", err)
		}
	}
	if _, err := a.callback(Relayed{URL: a.redirect + "?" + query, Code: "different"}, now); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("mixed input: %v", err)
	}
}
