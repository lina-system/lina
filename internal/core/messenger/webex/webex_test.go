// SPDX-License-Identifier: Apache-2.0

package webex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var source = messenger.SourceID{Platform: "webex", Account: "person-1"}

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

	first, err := adapter.Page(t.Context(), source, "room-1", "")
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

	second, err := adapter.Page(t.Context(), source, "room-1", first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.Messages[0].ID != "m-1" || second.Messages[1].ID != "m-2" {
		t.Fatalf("second page = %#v, want m-1 then m-2", second.Messages)
	}
	third, err := adapter.Page(t.Context(), source, "room-1", second.Next)
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
	tail, err := adapter.Page(t.Context(), source, "room-1", third.Next)
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

	backfill, err := adapter.Page(t.Context(), source, "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if backfill.HasMore {
		t.Fatal("a partial page announced older messages")
	}
	page, err := adapter.Page(t.Context(), source, "room-1", backfill.Next)
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
	next, err := adapter.Page(t.Context(), source, "room-1", page.Next)
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

	chats, err := adapter.ListChats(t.Context(), source)
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

	if _, err := adapter.ListChats(t.Context(), source); err != nil {
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

	state, err := adapter.SignIn(t.Context(), source)
	if err != nil {
		t.Fatalf("SignIn returned an error instead of a state: %v", err)
	}
	if state != messenger.NeedsSignIn {
		t.Fatalf("state = %s, want needs sign in", state)
	}
	if _, err := vault.GetTokens(webex.TokenKey(person)); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("revoked tokens remain: %v", err)
	}
	if _, err := adapter.ListChats(t.Context(), source); !errors.Is(err, messenger.ErrNeedsSignIn) {
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

	if _, err := adapter.ListChats(t.Context(), source); err != nil {
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
	chats, err := adapter.ListChats(t.Context(), source)
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
	if _, err := adapter.FetchAttachment(t.Context(), source, "room-1", "https://example.com/file.png"); err == nil {
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
	page, err := adapter.Page(t.Context(), source, "room-1", webex.TailCursorForTest("m-1"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(page.Messages); strings.Join(ids, ",") != "m-3,m-4" {
		t.Fatalf("first catch-up page = %v", ids)
	}
	if !page.HasMore || !strings.HasPrefix(string(page.Next), "webex/1/fill/") || !strings.HasSuffix(string(page.Next), "/"+webex.EncodeForTest("m-1")) {
		t.Fatalf("the catch-up cursor lost its stop: %q", page.Next)
	}
	next, err := adapter.Page(t.Context(), source, "room-1", page.Next)
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(next.Messages); strings.Join(ids, ",") != "m-2" {
		t.Fatalf("second catch-up page = %v, want only m-2 above the stop", ids)
	}
	if next.HasMore || string(next.Next) != string(webex.TailCursorForTest("m-4")) {
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

func TestReadRejectsAnotherAccount(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Now()}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		t.Error("account crossed into API read")
		http.NotFound(w, r)
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)
	if _, err := adapter.ListChats(t.Context(), messenger.SourceID{Platform: "webex", Account: "other"}); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSignInRejectsWrongIssuerAndPastedDestination(t *testing.T) {
	for _, kind := range []string{"issuer", "port", "path", "duplicate-state"} {
		t.Run(kind, func(t *testing.T) {
			rec := &recorder{}
			c := &clock{at: time.Now()}
			srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
			defer srv.Close()
			adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
			if err := adapter.Configure(t.Context(), clientID, clientSecret); err != nil {
				t.Fatal(err)
			}
			raw, err := adapter.BeginSignIn(t.Context(), 32123)
			if err != nil {
				t.Fatal(err)
			}
			parsed, _ := url.Parse(raw)
			state := parsed.Query().Get("state")
			redirect := webex.Redirect{Code: "code", State: state, Iss: "https://other.example"}
			if kind != "issuer" {
				target := "http://127.0.0.1:32123/auth/callback?code=code&state=" + state
				switch kind {
				case "port":
					target = strings.Replace(target, ":32123", ":32124", 1)
				case "path":
					target = strings.Replace(target, "/auth/callback", "/other", 1)
				case "duplicate-state":
					target += "&state=" + state
				}
				redirect = webex.Redirect{URL: target}
			}
			if _, err := adapter.CompleteSignIn(t.Context(), redirect); err == nil {
				t.Fatal("invalid callback accepted")
			}
			if len(rec.forms) != 0 {
				t.Fatal("invalid callback exchanged a code")
			}
		})
	}
}

func TestPagingRejectsForeignCollectionAndLoops(t *testing.T) {
	for _, suffix := range []string{"/v1/people/me", "/v1/rooms?max=2"} {
		t.Run(suffix, func(t *testing.T) {
			rec := &recorder{}
			c := &clock{at: time.Now()}
			var base string
			srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
				if len(rec.paths) > 2 {
					http.Error(w, "loop", 500)
					return
				}
				rec.add(r.URL.String())
				w.Header().Set("Link", "<"+base+suffix+">; rel=\"next\"")
				w.Write([]byte(`{"items":[{"id":"room","type":"group"}]}`))
			})
			defer srv.Close()
			base = srv.URL
			adapter, _ := newAdapter(t, base, c, rec, nil)
			signIn(t, adapter)
			if _, err := adapter.ListChats(t.Context(), source); err == nil {
				t.Fatal("invalid pagination accepted")
			}
			if len(rec.paths) > 1 {
				t.Fatal("repeated page fetched")
			}
		})
	}
}

func TestPartialPageFollowsHistoryLink(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Now()}
	var base string
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "<"+base+"/v1/messages?roomId=room-1&beforeMessage=m-2>; rel=\"next\"")
		w.Write([]byte(`{"items":[{"id":"m-2","roomId":"room-1","personId":"person-2","created":"2026-10-20T12:00:00Z","text":"partial page"}]}`))
	})
	defer srv.Close()
	base = srv.URL
	adapter, _ := newAdapter(t, base, c, rec, nil)
	signIn(t, adapter)
	page, err := adapter.Page(t.Context(), source, "room-1", "")
	if err != nil || !page.HasMore {
		t.Fatalf("older history lost: %+v %v", page, err)
	}
}

// An empty first read stores an unanchored tail. A later burst larger than one
// page must still reach the room's beginning before that anchor moves.
func TestEmptyRoomBurstBackfillsToTheBeginning(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	var newestReads int
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("beforeMessage") {
		case "":
			newestReads++
			if newestReads > 1 {
				w.Write([]byte(`{"items":[{"id":"m-3","roomId":"room-1","text":"third","personId":"person-2","created":"2026-10-20T12:03:00.000Z"},{"id":"m-2","roomId":"room-1","text":"second","personId":"person-2","created":"2026-10-20T12:02:00.000Z"}]}`))
				return
			}
			w.Write([]byte(`{"items":[]}`))
		case "m-2":
			w.Write([]byte(`{"items":[{"id":"m-1","roomId":"room-1","text":"first","personId":"person-2","created":"2026-10-20T12:01:00.000Z"}]}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	empty, err := adapter.Page(t.Context(), source, "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Messages) != 0 || empty.HasMore || empty.Next != webex.TailCursorForTest("") {
		t.Fatalf("empty room = %+v", empty)
	}
	var got []string
	cursor := empty.Next
	for range 4 {
		page, err := adapter.Page(t.Context(), source, "room-1", cursor)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, messageIDs(page.Messages)...)
		cursor = page.Next
		if !page.HasMore {
			break
		}
	}
	if strings.Join(got, ",") != "m-2,m-3,m-1" {
		t.Fatalf("burst messages = %v, want the newest page then the older message", got)
	}
	if cursor != webex.TailCursorForTest("m-3") {
		t.Fatalf("tail after the burst = %q, want the newest message", cursor)
	}
}

// A reply on a newer history page and its parent on the next older page have
// to be admitted together. The store links a reply only to a parent already
// admitted or present in the same page, and a later read of an unchanged reply
// does not repair the relation.
func TestReplyParentOnOlderPageSharesThePage(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("beforeMessage") {
		case "":
			w.Write([]byte(`{"items":[{"id":"m-4","roomId":"room-1","text":"fourth","personId":"person-2","created":"2026-10-20T12:04:00.000Z"},{"id":"m-3","roomId":"room-1","text":"reply","personId":"person-2","parentId":"m-1","created":"2026-10-20T12:03:00.000Z"}]}`))
		case "m-3":
			w.Write([]byte(`{"items":[{"id":"m-2","roomId":"room-1","text":"second","personId":"person-2","created":"2026-10-20T12:02:00.000Z"},{"id":"m-1","roomId":"room-1","text":"root","personId":"person-2","created":"2026-10-20T12:01:00.000Z"}]}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	page, err := adapter.Page(t.Context(), source, "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(page.Messages); strings.Join(ids, ",") != "m-1,m-2,m-3,m-4" {
		t.Fatalf("history page = %v, want the parent and the reply together", ids)
	}
	if page.Messages[0].ID != "m-1" || page.Messages[2].ReplyTo != "m-1" {
		t.Fatalf("reply = %#v", page.Messages)
	}
	if !page.HasMore {
		t.Fatal("a full older page did not keep the backfill open")
	}
}

// Catch-up walks back toward the previous anchor. A reply in that burst whose
// parent is also in the burst stays on the same page as its parent, and the
// anchor itself is not read again.
func TestCatchUpKeepsReplyParentBeforeTheStop(t *testing.T) {
	rec := &recorder{}
	c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
	srv := signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("beforeMessage") {
		case "":
			w.Write([]byte(`{"items":[{"id":"m-4","roomId":"room-1","text":"reply","personId":"person-2","parentId":"m-2","created":"2026-10-20T12:04:00.000Z"},{"id":"m-3","roomId":"room-1","text":"third","personId":"person-2","created":"2026-10-20T12:03:00.000Z"}]}`))
		case "m-3":
			w.Write([]byte(`{"items":[{"id":"m-2","roomId":"room-1","text":"root","personId":"person-2","created":"2026-10-20T12:02:00.000Z"},{"id":"m-1","roomId":"room-1","text":"anchor","personId":"person-2","created":"2026-10-20T12:01:00.000Z"}]}`))
		default:
			w.Write([]byte(`{"items":[]}`))
		}
	})
	defer srv.Close()
	adapter, _ := newAdapter(t, srv.URL, c, rec, nil)
	signIn(t, adapter)

	page, err := adapter.Page(t.Context(), source, "room-1", webex.TailCursorForTest("m-1"))
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(page.Messages); strings.Join(ids, ",") != "m-2,m-3,m-4" {
		t.Fatalf("catch-up page = %v, want the parent and the reply, not the anchor", ids)
	}
	if page.Messages[2].ReplyTo != "m-2" {
		t.Fatalf("reply = %#v", page.Messages[2])
	}
	if page.HasMore || page.Next != webex.TailCursorForTest("m-4") {
		t.Fatalf("catch-up cursor = %q hasMore=%v", page.Next, page.HasMore)
	}
}

// A reply whose parent is far behind the newest page must not be glued into one
// page larger than the admission query. The reply stays uncommitted until the
// walk has returned its parent, including when the two no longer fit together.
func TestCombinedHistoryStaysWithinTheAdmissionLimit(t *testing.T) {
	cases := []struct {
		name     string
		limit    int
		pageSize int
		count    int
		reply    string // "newest" replies to m-1; "all" does except m-1; "gone" replies to an absent id
		samePage bool
	}{
		{name: "far parent", limit: 4, pageSize: 2, count: 8, reply: "newest"},
		{name: "parent fits with the reply", limit: 8, pageSize: 2, count: 4, reply: "newest", samePage: true},
		{name: "replies overflow the parent page", limit: 3, pageSize: 2, count: 7, reply: "all"},
		{name: "reply chain stays behind its parents", limit: 3, pageSize: 2, count: 5, reply: "chain"},
		{name: "missing parent still finishes", limit: 4, pageSize: 2, count: 6, reply: "gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := make([]histMessage, tc.count)
			parents := map[string]string{}
			for i := range msgs {
				id := fmt.Sprintf("m-%d", i+1)
				msgs[i] = histMessage{id: id, text: id}
				switch tc.reply {
				case "newest":
					if i == tc.count-1 {
						msgs[i].parent = "m-1"
					}
				case "all":
					if i > 0 {
						msgs[i].parent = "m-1"
					}
				case "chain":
					switch i {
					case 1:
						msgs[i].parent = "m-1"
					case 2:
						msgs[i].parent = "m-2"
					case 4:
						msgs[i].parent = "m-3"
					}
				case "gone":
					msgs[i].parent = "gone"
				}
				parents[id] = msgs[i].parent
			}
			rec := &recorder{}
			c := &clock{at: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)}
			srv := serveHistory(t, rec, msgs, tc.pageSize)
			defer srv.Close()
			adapter, _ := newAdapter(t, srv.URL, c, rec, func(cfg *webex.Config) { cfg.PageSize = tc.pageSize })
			webex.SetCombinedLimitForTest(adapter, tc.limit)
			signIn(t, adapter)

			pages, tail := walkHistory(t, adapter, tc.limit, tc.count)
			seen := map[string]int{}
			for pi, page := range pages {
				for _, message := range page {
					if _, ok := seen[message.ID]; ok {
						t.Fatalf("duplicate %s", message.ID)
					}
					seen[message.ID] = pi
					if message.ReplyTo != parents[message.ID] {
						t.Fatalf("%s reply = %q, want %q", message.ID, message.ReplyTo, parents[message.ID])
					}
				}
			}
			if len(seen) != tc.count {
				t.Fatalf("walk returned %d messages, want %d", len(seen), tc.count)
			}
			newest := fmt.Sprintf("m-%d", tc.count)
			if tail != webex.TailCursorForTest(newest) {
				t.Fatalf("tail = %q, want %q", tail, webex.TailCursorForTest(newest))
			}
			if tc.reply == "gone" {
				return
			}
			for id, page := range seen {
				parent := parents[id]
				if parent == "" {
					continue
				}
				parentPage, ok := seen[parent]
				if !ok {
					t.Fatalf("%s parent %s was not admitted", id, parent)
				}
				if page < parentPage {
					t.Fatalf("%s admitted on page %d before its parent on page %d", id, page, parentPage)
				}
				if tc.samePage && page != parentPage {
					t.Fatalf("%s on page %d, want the parent's page %d", id, page, parentPage)
				}
			}
		})
	}
}

type histMessage struct {
	id, parent, text string
}

func serveHistory(t *testing.T, rec *recorder, msgs []histMessage, pageSize int) *httptest.Server {
	t.Helper()
	index := map[string]histMessage{}
	for _, message := range msgs {
		index[message.id] = message
	}
	return signInServer(t, rec, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages" {
			end := len(msgs)
			if before := r.URL.Query().Get("beforeMessage"); before != "" {
				end = -1
				for i, message := range msgs {
					if message.id == before {
						end = i
						break
					}
				}
				if end < 0 {
					w.Write([]byte(`{"items":[]}`))
					return
				}
			}
			start := end - pageSize
			if start < 0 {
				start = 0
			}
			items := make([]histMessage, 0, end-start)
			for i := end - 1; i >= start; i-- {
				items = append(items, msgs[i])
			}
			writeHistory(w, items)
			return
		}
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/messages/"); ok {
			id, err := url.PathUnescape(id)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			message, found := index[id]
			if !found {
				http.NotFound(w, r)
				return
			}
			raw, _ := json.Marshal(historyItem(message))
			w.Write(raw)
			return
		}
		http.NotFound(w, r)
	})
}

func writeHistory(w http.ResponseWriter, items []histMessage) {
	wire := make([]historyWire, len(items))
	for i, item := range items {
		wire[i] = historyItem(item)
	}
	raw, _ := json.Marshal(struct {
		Items []historyWire `json:"items"`
	}{Items: wire})
	w.Write(raw)
}

type historyWire struct {
	ID       string `json:"id"`
	RoomID   string `json:"roomId"`
	Text     string `json:"text"`
	PersonID string `json:"personId"`
	Created  string `json:"created"`
	ParentID string `json:"parentId,omitempty"`
}

func historyItem(message histMessage) historyWire {
	n := 0
	fmt.Sscanf(message.id, "m-%d", &n)
	created := time.Date(2026, 10, 20, 12, 0, n, 0, time.UTC).Format(time.RFC3339Nano)
	return historyWire{ID: message.id, RoomID: "room-1", Text: message.text, PersonID: "person-2", Created: created, ParentID: message.parent}
}

func walkHistory(t *testing.T, adapter *webex.Adapter, limit, count int) ([][]messenger.Message, messenger.Cursor) {
	t.Helper()
	var pages [][]messenger.Message
	cursor := messenger.Cursor("")
	for range count*4 + 8 {
		page, err := adapter.Page(t.Context(), source, "room-1", cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Messages) > limit {
			t.Fatalf("page has %d messages, limit %d: %v", len(page.Messages), limit, messageIDs(page.Messages))
		}
		pages = append(pages, page.Messages)
		if !page.HasMore {
			return pages, page.Next
		}
		if page.Next == "" || page.Next == cursor {
			t.Fatalf("cursor did not advance from %q", cursor)
		}
		cursor = page.Next
	}
	t.Fatal("walk did not finish")
	return nil, ""
}
