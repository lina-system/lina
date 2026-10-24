// SPDX-License-Identifier: Apache-2.0
package teams

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/kit/secret"
)

const tenant = "11111111-1111-4111-8111-111111111111"
const clientID = "22222222-2222-4222-8222-222222222222"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func adapter(t *testing.T, f transportFunc) *Adapter {
	t.Helper()
	return NewAdapter(Config{Tenant: tenant, Account: tenant + ":owner", Token: func(context.Context, bool) (secret.Secret, error) { return secret.New("synthetic-token"), nil }, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "graph.microsoft.com" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		return f(r)
	})})
}
func TestChatPagingEditsDeletionsAndProfiles(t *testing.T) {
	calls := 0
	a := adapter(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1.0/chats/chat/messages":
			calls++
			if r.URL.Query().Get("$orderby") != "lastModifiedDateTime desc" {
				t.Error("missing matching orderby")
			}
			if calls == 1 {
				return response(200, `{"value":[{"id":"m2","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-23T12:00:00Z","from":{"user":{"id":"other","tenantId":"`+tenant+`","displayName":"Someone"}},"body":{"contentType":"html","content":"<p>Edited &amp; safe</p>"}}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/chats/chat/messages?$skiptoken=next&$orderby=lastModifiedDateTime%20desc"}`), nil
			}
			if calls == 2 {
				return response(200, `{"value":[{"id":"m1","createdDateTime":"2026-10-20T09:00:00Z","lastModifiedDateTime":"2026-10-22T12:00:00Z","deletedDateTime":"2026-10-22T12:00:00Z"}]}`), nil
			}
			if !strings.Contains(r.URL.Query().Get("$filter"), "lastModifiedDateTime gt 2026-10-23T11:59:59Z") {
				t.Error("lost high watermark", r.URL.Query())
			}
			return response(200, `{"value":[]}`), nil
		case "/v1.0/users/other":
			return response(200, `{"id":"other","displayName":"Someone","mail":"someone@example.test"}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", r.URL)
			return nil, nil
		}
	})
	p, err := a.Page(t.Context(), "chat:chat", "")
	if err != nil || !p.HasMore || len(p.Messages) != 1 {
		t.Fatalf("page: %+v %v", p, err)
	}
	m := p.Messages[0]
	if m.Author.ID != tenant+":other" || m.Author.Email != "someone@example.test" || m.Route != messenger.RouteParticipant || strings.TrimSpace(m.Text) != "Edited & safe" || m.Edited.IsZero() {
		t.Fatalf("message: %+v", m)
	}
	p, err = a.Page(t.Context(), "chat:chat", p.Next)
	if err != nil || p.HasMore || len(p.Deletions) != 1 || p.Deletions[0].Message != "m1" {
		t.Fatalf("deletion page %+v %v", p, err)
	}
	if _, err = a.Page(t.Context(), "chat:chat", p.Next); err != nil {
		t.Fatal(err)
	}
}
func TestGraphNextLinkCannotEscapeCollection(t *testing.T) {
	for _, next := range []string{"https://evil.example/messages", "http://graph.microsoft.com/v1.0/chats/chat/messages", "https://graph.microsoft.com/v1.0/chats/other/messages", "https://graph.microsoft.com:443/v1.0/chats/chat/messages", "https://graph.microsoft.com/v1.0/chats/chat/messages#fragment"} {
		t.Run(next, func(t *testing.T) {
			calls := 0
			a := adapter(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, `{"value":[],"@odata.nextLink":"`+next+`"}`), nil
			})
			_, err := a.Page(t.Context(), "chat:chat", "")
			if !errors.Is(err, ErrDestination) || calls != 1 {
				t.Fatalf("escape: calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestThrottleAndConsent(t *testing.T) {
	calls := 0
	var waited time.Duration
	a := adapter(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			r := response(429, `{"error":{"code":"TooManyRequests"}}`)
			r.Header.Set("Retry-After", "7")
			return r, nil
		}
		return response(200, `{"value":[]}`), nil
	})
	a.wait = func(_ context.Context, d time.Duration) error { waited = d; return nil }
	if _, err := a.Page(t.Context(), "chat:chat", ""); err != nil || waited != 7*time.Second || calls != 2 {
		t.Fatalf("retry %v %v %d", err, waited, calls)
	}
	a = adapter(t, func(*http.Request) (*http.Response, error) {
		return response(403, `{"error":{"code":"Forbidden","message":"private diagnostic"}}`), nil
	})
	if _, err := a.Page(t.Context(), "chat:chat", ""); !errors.Is(err, ErrUnsupported) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}
func TestChannelsIncludePagedRepliesWithoutUnsupportedFilter(t *testing.T) {
	a := adapter(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Has("$filter") || r.URL.Query().Has("$orderby") {
			t.Fatal("unsupported channel query", r.URL)
		}
		switch r.URL.Path {
		case "/v1.0/teams/team/channels/channel/messages":
			return response(200, `{"value":[{"id":"root","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-20T10:00:00Z","messageType":"systemEventMessage","body":{"content":"notice"}}]}`), nil
		case "/v1.0/teams/team/channels/channel/messages/root/replies":
			if r.URL.Query().Get("$skiptoken") == "next" {
				return response(200, `{"value":[{"id":"deleted","deletedDateTime":"2026-10-23T12:00:00Z","lastModifiedDateTime":"2026-10-23T12:00:00Z"}]}`), nil
			}
			return response(200, `{"value":[{"id":"reply","replyToId":"root","createdDateTime":"2026-10-21T10:00:00Z","lastModifiedDateTime":"2026-10-23T12:00:00Z","from":{"application":{"id":"bot","displayName":"Bot"}},"body":{"content":"edited reply"}}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/teams/team/channels/channel/messages/root/replies?$skiptoken=next"}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", r.URL)
			return nil, nil
		}
	})
	p, err := a.Page(t.Context(), "channel:team:channel", "")
	if err != nil || p.HasMore || len(p.Messages) != 2 || len(p.Deletions) != 1 || p.Messages[1].ReplyTo != "root" || p.Messages[1].Route != messenger.RouteAutomation {
		t.Fatalf("channel %+v %v", p, err)
	}
}

func TestRefreshOnceAfterUnauthorizedAndRedirectsNeverFollowed(t *testing.T) {
	calls, refreshes := 0, 0
	a := adapter(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(401, `{}`), nil
		}
		if calls == 2 {
			r := response(429, `{}`)
			r.Header.Set("Retry-After", "0")
			return r, nil
		}
		return response(200, `{"value":[]}`), nil
	})
	a.cfg.Token = func(_ context.Context, force bool) (secret.Secret, error) {
		if force {
			refreshes++
		}
		return secret.New("synthetic-token"), nil
	}
	if _, err := a.Page(t.Context(), "chat:chat", ""); err != nil || refreshes != 1 || calls != 3 {
		t.Fatalf("refresh=%d calls=%d err=%v", refreshes, calls, err)
	}
	calls = 0
	a = adapter(t, func(*http.Request) (*http.Response, error) {
		calls++
		r := response(302, "")
		r.Header.Set("Location", "https://evil.example/steal")
		return r, nil
	})
	if _, err := a.Page(t.Context(), "chat:chat", ""); err == nil || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
}
func TestLongRetryAfterReturnsDeadlineAndCancellationDoesNotAdvance(t *testing.T) {
	a := adapter(t, func(*http.Request) (*http.Response, error) {
		r := response(429, `{}`)
		r.Header.Set("Retry-After", "120")
		return r, nil
	})
	p, err := a.Page(t.Context(), "chat:chat", "")
	var retry *RetryError
	if !errors.As(err, &retry) || retry.After != 2*time.Minute || p.Next != "" {
		t.Fatalf("retry %+v %v", p, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.wait = pause
	if err := a.wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestEncodedGraphLinksAndDeletedUser(t *testing.T) {
	calls := 0
	a := adapter(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/v1.0/users/") {
			return response(404, `{}`), nil
		}
		calls++
		if calls == 1 {
			return response(200, `{"value":[],"@odata.nextLink":"https://graph.microsoft.com/v1.0/chats/19%3Achat%40thread.v2/messages?$skiptoken=next"}`), nil
		}
		return response(200, `{"value":[{"id":"m1","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-20T10:00:00Z","from":{"user":{"id":"deleted","displayName":"Former member"}},"body":{"content":"retained history"}}]}`), nil
	})
	p, err := a.Page(t.Context(), "chat:19:chat@thread.v2", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err = a.Page(t.Context(), "chat:19:chat@thread.v2", p.Next)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].Author.ID != tenant+":deleted" || p.Messages[0].Author.Email != "" {
		t.Fatal(p, err)
	}
}
