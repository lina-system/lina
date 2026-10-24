// SPDX-License-Identifier: Apache-2.0

package webex_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

const (
	person       = "person:test"
	accessOne    = "synthetic-access-1"
	refreshOne   = "synthetic-refresh-1"
	accessTwo    = "synthetic-access-2"
	refreshTwo   = "synthetic-refresh-2"
	clientID     = "synthetic-client"
	clientSecret = "synthetic-secret"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

type recorder struct {
	mu     sync.Mutex
	paths  []string
	forms  []url.Values
	auth   []string
	sleeps []time.Duration
}

func (r *recorder) add(path string) {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()
}

func (r *recorder) addAuth(value string) {
	r.mu.Lock()
	r.auth = append(r.auth, value)
	r.mu.Unlock()
}

func (r *recorder) addForm(values url.Values) {
	r.mu.Lock()
	r.forms = append(r.forms, values)
	r.mu.Unlock()
}

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.sleeps = append(r.sleeps, d)
	r.mu.Unlock()
	return ctx.Err()
}

func newAdapter(t *testing.T, baseURL string, c *clock, rec *recorder, configure func(*webex.Config)) (*webex.Adapter, *secrets.Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	cfg := webex.Config{Person: person, Vault: vault, BaseURL: baseURL, Now: c.now, Sleep: rec.sleep, PageSize: 2}
	if configure != nil {
		configure(&cfg)
	}
	adapter, err := webex.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, vault
}

// signIn stores the recorded token response through the OAuth code flow, so
// expiry handling follows production rather than a test hook.
func signIn(t *testing.T, adapter *webex.Adapter) {
	t.Helper()
	if err := adapter.Configure(t.Context(), clientID, clientSecret); err != nil {
		t.Fatal(err)
	}
	raw, err := adapter.BeginSignIn(t.Context(), 32123)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	account, err := adapter.CompleteSignIn(t.Context(), webex.Redirect{Code: "synthetic-code", State: parsed.Query().Get("state")})
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "person-1" || account.Email != "jun@example.com" {
		t.Fatalf("account = %#v", account)
	}
}

// signInServer serves the token endpoint and /people/me from recorded
// responses; each test adds only the API routes it needs.
func signInServer(t *testing.T, rec *recorder, handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/access_token":
			parseForm(r, rec)
			if r.PostForm.Get("grant_type") == "refresh_token" {
				w.Write(fixture(t, "token-refreshed.json"))
				return
			}
			w.Write(fixture(t, "token.json"))
			return
		case "/v1/people/me":
			w.Write(fixture(t, "people-me.json"))
			return
		}
		handler(w, r)
	}))
}

func parseForm(r *http.Request, rec *recorder) {
	_ = r.ParseForm()
	rec.addForm(r.PostForm)
}

func TestPageReadsOlderMessages(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		rec.add(r.URL.Query().Get("beforeMessage"))
		if got := r.URL.Query().Get("roomId"); got != "room-1" {
			t.Errorf("roomId = %q", got)
		}
		switch r.URL.Query().Get("beforeMessage") {
		case "":
			w.Write(fixture(t, "messages-page-1.json"))
		case "m-3":
			w.Write(fixture(t, "messages-page-2.json"))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	first, err := adapter.Page(t.Context(), "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 2 || first.Messages[0].ID != "m-3" || first.Messages[1].ID != "m-4" {
		t.Fatalf("first page = %#v, want m-3 then m-4", first.Messages)
	}
	if !first.HasMore || first.Next == "" {
		t.Fatal("first page did not announce older messages")
	}
	if first.Messages[0].At.After(first.Messages[1].At) {
		t.Fatal("a page is not oldest first")
	}
	if first.Messages[0].Route != messenger.RouteParticipant || first.Messages[0].Author.ID != "person-2" {
		t.Fatalf("route/author = %s/%s", first.Messages[0].Route, first.Messages[0].Author.ID)
	}

	second, err := adapter.Page(t.Context(), "room-1", first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.Messages[0].ID != "m-1" || second.Messages[1].ID != "m-2" {
		t.Fatalf("second page = %#v, want m-1 then m-2", second.Messages)
	}
	third, err := adapter.Page(t.Context(), "room-1", second.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Messages) != 0 || third.HasMore {
		t.Fatalf("final backfill page = %#v", third)
	}
	want := []string{"", "m-3", "m-1"}
	if strings.Join(rec.paths, ",") != strings.Join(want, ",") {
		t.Fatalf("beforeMessage requests = %v, want %v", rec.paths, want)
	}

	// The finished backfill hands over to the tail, which reads the newest
	// page. Re-serving what was already read is how an edit arrives; the store
	// ignores a message it already holds.
	tail, err := adapter.Page(t.Context(), "room-1", third.Next)
	if err != nil {
		t.Fatal(err)
	}
	if tail.HasMore || string(tail.Next) != string(third.Next) {
		t.Fatalf("tail cursor = %q, want the settled tail %q", tail.Next, third.Next)
	}
	for _, message := range tail.Messages {
		switch message.ID {
		case "m-1", "m-2", "m-3", "m-4":
		default:
			t.Fatalf("the tail returned an unknown message %q", message.ID)
		}
	}
}

func TestPageTailAdmitsNewMessagesAndEdits(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	var reads int
	var mu sync.Mutex
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("beforeMessage") != "" {
			w.Write([]byte(`{"items":[]}`))
			return
		}
		mu.Lock()
		reads++
		read := reads
		mu.Unlock()
		if read == 1 {
			w.Write(fixture(t, "messages-page-1.json"))
			return
		}
		w.Write(fixture(t, "messages-tail.json"))
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, func(cfg *webex.Config) { cfg.PageSize = 3 })
	signIn(t, adapter)

	backfill, err := adapter.Page(t.Context(), "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if backfill.HasMore {
		t.Fatal("a partial page announced older messages")
	}
	page, err := adapter.Page(t.Context(), "room-1", backfill.Next)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(page.Messages))
	for _, message := range page.Messages {
		ids = append(ids, message.ID)
	}
	if strings.Join(ids, ",") != "m-4,m-5,m-6" {
		t.Fatalf("tail messages = %v, want m-4 (edited), m-5, m-6", ids)
	}
	for _, message := range page.Messages {
		if message.ID == "m-4" && message.Edited.IsZero() {
			t.Fatal("the edited message lost its edit time")
		}
	}
	if page.HasMore {
		t.Fatal("tail page announced more messages")
	}
	// A second tail read returns the same newest page, which carries the edit
	// of m-4 so a later edit is picked up; the store admits only the newer
	// revision.
	next, err := adapter.Page(t.Context(), "room-1", page.Next)
	if err != nil {
		t.Fatal(err)
	}
	if string(next.Next) != string(page.Next) {
		t.Fatalf("second tail read moved to %q, want %q", next.Next, page.Next)
	}
	if len(next.Messages) != 3 || next.Messages[2].ID != "m-6" {
		t.Fatalf("second tail read = %#v", next.Messages)
	}
}

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	var hits int
	var mu sync.Mutex
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		hits++
		hit := hits
		mu.Unlock()
		if hit == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.Write(fixture(t, "rooms.json"))
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	chats, err := adapter.ListChats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].ID != "room-1" {
		t.Fatalf("chats = %#v", chats)
	}
	if len(rec.sleeps) != 1 || rec.sleeps[0] != 3*time.Second {
		t.Fatalf("waits = %v, want [3s]", rec.sleeps)
	}
}

func TestRefreshBeforeExpiryRotatesStoredTokens(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rooms":
			rec.addAuth(r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer "+accessTwo {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write(fixture(t, "rooms.json"))
		default:
			http.NotFound(w, r)
		}
	})
	defer srv.Close()
	adapter, vault := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)
	c.advance(15 * 24 * time.Hour)

	if _, err := adapter.ListChats(t.Context()); err != nil {
		t.Fatal(err)
	}
	tokens, err := vault.GetTokens(webex.TokenKey(person))
	if err != nil || !tokens.Valid(true) {
		t.Fatalf("rotated tokens are missing: %v", err)
	}
	if len(rec.forms) != 2 {
		t.Fatalf("token exchanges = %d, want sign-in and one refresh", len(rec.forms))
	}
	refresh := rec.forms[1]
	if refresh.Get("grant_type") != "refresh_token" || refresh.Get("refresh_token") != refreshOne {
		t.Fatalf("refresh form = %v", refresh)
	}
	if len(rec.auth) == 0 || rec.auth[len(rec.auth)-1] != "Bearer "+accessTwo {
		t.Fatalf("authorization values = %v", rec.auth)
	}
}

func TestRevokedTokenNeedsSignIn(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	var refreshes int
	var mu sync.Mutex
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms" {
			http.NotFound(w, r)
			return
		}
		w.Write(fixture(t, "rooms.json"))
	})
	defer srv.Close()
	// Replace the default token endpoint with one that revokes refresh tokens.
	signInSrv := srv.Config.Handler
	revoking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/access_token" {
			_ = r.ParseForm()
			rec.addForm(r.PostForm)
			mu.Lock()
			refreshes++
			refresh := refreshes > 1
			mu.Unlock()
			if refresh {
				w.WriteHeader(http.StatusBadRequest)
				w.Write(fixture(t, "token-revoked.json"))
				return
			}
			w.Write(fixture(t, "token.json"))
			return
		}
		signInSrv.ServeHTTP(w, r)
	}))
	defer revoking.Close()
	adapter, vault := newAdapter(t, revoking.URL, c, rec, nil)
	signIn(t, adapter)
	c.advance(15 * 24 * time.Hour)

	state, err := adapter.SignIn(t.Context())
	if err != nil {
		t.Fatalf("SignIn returned an error instead of a state: %v", err)
	}
	if state != messenger.NeedsSignIn {
		t.Fatalf("state = %s, want needs sign in", state)
	}
	if _, err := vault.GetTokens(webex.TokenKey(person)); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("revoked tokens remain: %v", err)
	}
	if _, err := adapter.ListChats(t.Context()); !errors.Is(err, messenger.ErrNeedsSignIn) {
		t.Fatalf("ListChats error = %v, want needs sign in", err)
	}
}

func TestUnauthorizedRefreshesOnce(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	var rejected bool
	var mu sync.Mutex
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rooms":
			auth := r.Header.Get("Authorization")
			rec.addAuth(auth)
			mu.Lock()
			reject := !rejected && auth == "Bearer "+accessOne
			if reject {
				rejected = true
			}
			mu.Unlock()
			if reject || auth != "Bearer "+accessTwo {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write(fixture(t, "rooms.json"))
		default:
			http.NotFound(w, r)
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	if _, err := adapter.ListChats(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(rec.auth) != 2 || rec.auth[0] != "Bearer "+accessOne || rec.auth[1] != "Bearer "+accessTwo {
		t.Fatalf("authorization values = %v", rec.auth)
	}
}

func TestListChatsFollowsLink(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", "<http://"+r.Host+"/v1/rooms?page=2>; rel=\"next\"")
			w.Write(fixture(t, "rooms.json"))
			return
		}
		w.Write(fixture(t, "rooms-next.json"))
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)
	chats, err := adapter.ListChats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, chat := range chats {
		ids = append(ids, chat.ID)
	}
	if strings.Join(ids, ",") != "room-1,room-2" {
		t.Fatalf("chats = %v", ids)
	}
}

func TestAttachmentsStayOnWebexHosts(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)
	if _, err := adapter.FetchAttachment(t.Context(), "room-1", "https://example.com/file.png"); err == nil {
		t.Fatal("an attachment from another host was fetched")
	}
}

func TestConfigRequiresAPersonAndVault(t *testing.T) {
	if _, err := webex.New(webex.Config{}); err == nil {
		t.Fatal("a config without a person and vault was accepted")
	}
}

func TestTailCatchUpKeepsEveryMessage(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		// Two messages of the room after m-1, then the page that reaches m-1.
		rec.add(r.URL.Query().Get("beforeMessage"))
		switch r.URL.Query().Get("beforeMessage") {
		case "":
			w.Write([]byte(`{"items":[{"id":"m-4","roomId":"room-1","text":"fourth","personId":"person-2","created":"2026-10-20T12:04:00.000Z"},{"id":"m-3","roomId":"room-1","text":"third","personId":"person-2","created":"2026-10-20T12:03:00.000Z"}]}`))
		case "m-3":
			w.Write([]byte(`{"items":[{"id":"m-2","roomId":"room-1","text":"second","personId":"person-2","created":"2026-10-20T12:02:00.000Z"},{"id":"m-1","roomId":"room-1","text":"first","personId":"person-2","created":"2026-10-20T12:01:00.000Z"}]}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, func(cfg *webex.Config) { cfg.PageSize = 2 })
	signIn(t, adapter)

	// The last read ended at m-1; more than one page arrived since.
	page, err := adapter.Page(t.Context(), "room-1", webex.TailCursorForTest("m-1"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(page.Messages); strings.Join(ids, ",") != "m-3,m-4" {
		t.Fatalf("first catch-up page = %v", ids)
	}
	if !page.HasMore || !strings.HasPrefix(string(page.Next), "webex/1/fill/") || !strings.HasSuffix(string(page.Next), "/"+webex.EncodeForTest("m-1")) {
		t.Fatalf("the catch-up cursor lost its stop: %q", page.Next)
	}
	next, err := adapter.Page(t.Context(), "room-1", page.Next)
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(next.Messages); strings.Join(ids, ",") != "m-2" {
		t.Fatalf("second catch-up page = %v, want only m-2 above the stop", ids)
	}
	if next.HasMore || string(next.Next) != string(webex.TailCursorForTest("m-2")) {
		t.Fatalf("catch-up did not hand over to the tail: %q", next.Next)
	}
}

func TestCursorKeepsSlashInsideAnID(t *testing.T) {
	id := "Y2lzY29zcGFyazovL3VzL01FU1NBR0Uv/core+1"
	cursor := webex.FillCursorForTest(id, "newest/id")
	before, newest, err := webex.ParseCursorForTest(cursor)
	if err != nil || before != id || newest != "newest/id" {
		t.Fatalf("cursor %q round-tripped to %q/%q: %v", cursor, before, newest, err)
	}
	if before, newest, err := webex.ParseCursorForTest("webex/1/unknown/x"); err == nil || before != "" || newest != "" {
		t.Fatal("an unknown cursor mode was accepted")
	}
}

func messageIDs(messages []messenger.Message) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}
