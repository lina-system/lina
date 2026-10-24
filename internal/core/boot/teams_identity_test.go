// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger/teams"
)

const (
	teamsTenant   = "11111111-1111-4111-8111-111111111111"
	teamsClientID = "22222222-2222-4222-8222-222222222222"
)

// teamsTransport sends Microsoft's login and Graph origins to their test
// servers, so the service is composed exactly as Core composes it.
type teamsTransport struct{ login, graph *url.URL }

func (t teamsTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	target := t.graph
	if r.URL.Host == "login.microsoftonline.com" {
		target = t.login
	} else if r.URL.Host != "graph.microsoft.com" {
		return nil, fmt.Errorf("unexpected host %q", r.URL.Host)
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
	clone.Host = target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// TestTeamsAdmissionCreatesSenderIdentity proves a message admitted by the
// Teams reader, with no MessengerConfig, records its author as a sender
// identity, and that a people record committed after the messages arrived
// links on the reader's next pass.
func TestTeamsAdmissionCreatesSenderIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	runtime := identityBoot(t)
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/"+teamsTenant+"/oauth2/v2.0/token" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"access-fixture","refresh_token":"refresh-fixture","token_type":"Bearer","expires_in":3600,"scope":"User.Read User.ReadBasic.All Chat.Read ChatMember.Read Team.ReadBasic.All Channel.ReadBasic.All ChannelMessage.Read.All ChannelMember.Read.All offline_access"}`)
	}))
	defer login.Close()
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/me":
			_, _ = io.WriteString(w, `{"id":"owner"}`)
		case "/v1.0/me/chats":
			_, _ = io.WriteString(w, `{"value":[{"id":"chat","topic":"Project","chatType":"group"}]}`)
		case "/v1.0/me/joinedTeams":
			_, _ = io.WriteString(w, `{"value":[]}`)
		case "/v1.0/chats/chat/messages":
			_, _ = io.WriteString(w, `{"value":[{"id":"m-1","createdDateTime":"2026-10-20T12:06:00Z","from":{"user":{"id":"person-2","tenantId":"`+teamsTenant+`","displayName":"Mira"}},"body":{"contentType":"text","content":"hello"}}]}`)
		case "/v1.0/users/person-2":
			_, _ = io.WriteString(w, `{"id":"person-2","displayName":"Mira","mail":"mira@example.com"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer graph.Close()
	loginURL, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	graphURL, err := url.Parse(graph.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.initTeams(nil, teamsTransport{login: loginURL, graph: graphURL}); err != nil {
		t.Fatal(err)
	}
	defer runtime.teams.Close()
	if err := runtime.teams.Configure(ctx, teams.Connection{ID: "work", Tenant: teamsTenant, ClientID: teamsClientID}); err != nil {
		t.Fatal(err)
	}
	begin, err := runtime.teams.Begin(ctx, "work", "session", 8400)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(begin)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.teams.Complete(ctx, "session", "synthetic-code", parsed.Query().Get("state"), "", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		senders, links, records, err := identityRows(ctx, runtime.db, "teams", teamsTenant+":person-2")
		if err != nil {
			t.Fatal(err)
		}
		if senders >= 1 {
			if links != 0 {
				t.Fatalf("links = %d, want none before the people record exists", links)
			}
			if records == 0 {
				t.Fatalf("records = 0, want the Teams message admitted")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("senders = %d records = %d, want the Teams author recorded as an identity", senders, records)
		}
		time.Sleep(20 * time.Millisecond)
	}
	addMira(t, runtime)
	// The reader's next pass matches the identity the messages already created.
	if err := runtime.teams.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, links, _, err := identityRows(ctx, runtime.db, "teams", teamsTenant+":person-2")
		if err != nil {
			t.Fatal(err)
		}
		if links == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("links = %d, want the exact link to person-mira after the record arrived", links)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
