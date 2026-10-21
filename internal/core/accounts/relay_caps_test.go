// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestRelaySelectsOnlyTheOwnersSavedRegistration(t *testing.T) {
	now := time.Unix(1800000000, 0)
	flow := newPendingSignIns(func() time.Time { return now }, nil)
	caps := RelayCaps{ConnectionID: "connection-one", CanListen: true, Port: 32123, Purpose: Login, AccountID: "account-one"}
	registration := signInRegistration{accountID: "account-one", person: "owner", provider: provider.ChatGPT, issuer: openAIIssuer, subject: "subject-one", clientID: "oaiapp_saved", hostID: "host-one"}
	pending, err := flow.beginRelay(t.Context(), provider.ChatGPT, "owner", "host-one", caps, &registration)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(pending.URL)
	if parsed.Query().Get("client_id") != "oaiapp_saved" || parsed.Query().Get("ext_agent_host_id") != "host-one" {
		t.Fatal("saved registration not reused")
	}
	for _, test := range []struct {
		name   string
		mutate func(*signInRegistration)
	}{
		{"person", func(r *signInRegistration) { r.person = "other" }},
		{"account", func(r *signInRegistration) { r.accountID = "other" }},
		{"provider", func(r *signInRegistration) { r.provider = provider.Gemini }},
		{"issuer", func(r *signInRegistration) { r.issuer = "https://other.invalid" }},
		{"host", func(r *signInRegistration) { r.hostID = "other" }},
		{"missing subject", func(r *signInRegistration) { r.subject = "" }},
		{"missing client", func(r *signInRegistration) { r.clientID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := registration
			test.mutate(&bad)
			if _, err := flow.beginRelay(t.Context(), provider.ChatGPT, "owner", "host-one", caps, &bad); !errors.Is(err, ErrSignInInvalid) {
				t.Fatalf("foreign registration admitted: %v", err)
			}
		})
	}
	if _, err := flow.beginRelay(t.Context(), provider.ChatGPT, "owner", "host-one", caps, nil); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("unknown account: %v", err)
	}
}

func TestClientWithoutListenerGetsTheSameFixedCallbackPath(t *testing.T) {
	now := time.Unix(1800000000, 0)
	flow := newPendingSignIns(func() time.Time { return now }, nil)
	caps := RelayCaps{ConnectionID: "connection-one", Purpose: AddProvider}
	pending, err := flow.beginRelay(t.Context(), provider.ChatGPT, "owner", "host-one", caps, nil)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(pending.URL)
	callback, _ := url.Parse(parsed.Query().Get("redirect_uri"))
	if callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != SignInCallbackPath {
		t.Fatal("paste fallback changed the redirect contract")
	}
	for _, bad := range []RelayCaps{
		{ConnectionID: "connection-one", Purpose: Login, CanListen: true},
		{ConnectionID: "connection-one", Purpose: AddProvider, CanListen: false, Port: 32123},
		{ConnectionID: "connection-one", Purpose: "unknown"},
		{Purpose: Login},
	} {
		if _, err := flow.beginRelay(t.Context(), provider.ChatGPT, "owner", "host-one", bad, nil); !errors.Is(err, ErrSignInInvalid) {
			t.Fatalf("invalid relay capability: %v", err)
		}
	}
	if _, err := flow.beginRelay(t.Context(), provider.Gemini, "owner", "host-one", caps, nil); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("unsupported sign-in provider: %v", err)
	}
}
