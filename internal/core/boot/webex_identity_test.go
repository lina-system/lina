// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger/webex"
)

// fixtureTransport sends Webex's production origin to the test server, so the
// service is composed exactly as Core composes it.
type fixtureTransport struct{ target *url.URL }

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = f.target.Scheme, f.target.Host
	clone.Host = f.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// TestWebexAdmissionCreatesSenderIdentity proves a message admitted by the
// Webex service, with no MessengerConfig, records its author as a sender
// identity and links it exactly to the people record whose contact holds the
// author's email.
func TestWebexAdmissionCreatesSenderIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	runtime := identityBoot(t)
	addMira(t, runtime)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/access_token":
			_, _ = io.WriteString(w, `{"access_token":"access-fixture","refresh_token":"refresh-fixture","expires_in":3600,"refresh_token_expires_in":3600,"token_type":"Bearer"}`)
		case "/v1/people/me":
			_, _ = io.WriteString(w, `{"id":"person-1","displayName":"Owner","emails":["owner@example.com"]}`)
		case "/v1/rooms":
			_, _ = io.WriteString(w, `{"items":[{"id":"room-1","title":"Project","type":"group","created":"2026-10-01T09:00:00.000Z","lastActivity":"2026-10-20T12:06:00.000Z"}]}`)
		case "/v1/messages":
			_, _ = io.WriteString(w, `{"items":[{"id":"m-1","roomId":"room-1","roomType":"group","text":"hello","personId":"person-2","personEmail":"mira@example.com","created":"2026-10-20T12:06:00.000Z"}]}`)
		case "/v1/memberships":
			_, _ = io.WriteString(w, `{"items":[{"id":"membership-1","roomId":"room-1","personId":"person-1","personEmail":"owner@example.com","personDisplayName":"Owner"},{"id":"membership-2","roomId":"room-1","personId":"person-2","personEmail":"mira@example.com","personDisplayName":"Mira"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.initWebex(nil, fixtureTransport{target: target}); err != nil {
		t.Fatal(err)
	}
	defer runtime.webex.Close()
	if err := runtime.webex.Configure(ctx, "client-fixture", "secret-fixture"); err != nil {
		t.Fatal(err)
	}
	begin, err := runtime.webex.Begin(ctx, "connection", 8400)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(begin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.webex.Complete(ctx, "connection", webex.Redirect{Code: "synthetic-code", State: parsed.Query().Get("state")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		senders, links, records, err := identityRows(ctx, runtime.db, "webex", "person-2")
		if err != nil {
			t.Fatal(err)
		}
		if senders >= 1 {
			if links != 1 {
				t.Fatalf("links = %d, want the exact link to person-mira", links)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("senders = %d records = %d, want the Webex author recorded as an identity", senders, records)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
