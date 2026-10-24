// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
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

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// TestM1WebexSourceInstalledCore proves a Webex source in a running Core. The
// person configures their own integration, signs in through the client's
// browser relay against a synthetic Webex, and Core reads the account's rooms
// and messages into the ledger on its own. A later read hits a rate limit with
// Retry-After; then the refresh token is revoked and the source turns needs
// sign-in without failing Core or the stored records.
func TestM1WebexSourceInstalledCore(t *testing.T) {
	t.Parallel()

	peer := newSyntheticWebex(t)
	h := newHarness(t, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"webex-host"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Core runs in this process for the Webex origin seam; each stage stops it
	// before the scenario reads canon, because the state root is locked.
	start := func() func() {
		ctx, cancel := context.WithCancel(h.ctx)
		done := make(chan error, 1)
		go func() {
			done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Messenger: &boot.MessengerConfig{
				Interval: 20 * time.Millisecond, WebexBaseURL: peer.server.URL, WebexInterval: 20 * time.Millisecond,
			}})
		}()
		waitForSocket(t, h.socket)
		waitCoreReady(t, h)
		return func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("webex Core stopped with %v", err)
				}
			case <-time.After(operationTimeout):
				t.Fatal("webex Core shutdown timed out")
			}
		}
	}
	withStore := func(check func(*canon.Store)) {
		t.Helper()
		db, closeStore := installedAcceptanceStore(t, h)
		defer closeStore()
		check(db)
	}

	// Stage 1: the person configures their own integration and signs in; Core
	// lists the room and reads its messages into the ledger.
	stop := start()
	setup := runMessengerClient(t, h.socket, "setup")
	if setup.Status != "connected" || setup.Platform != "webex" {
		t.Fatalf("sign-in result = %+v", setup)
	}
	if setup.Authorize == "" || !strings.HasPrefix(setup.Authorize, peer.server.URL+"/v1/authorize?") {
		t.Fatalf("authorization URL = %q", setup.Authorize)
	}
	parsed, err := url.Parse(setup.Authorize)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("code_challenge_method") != "S256" || parsed.Query().Get("code_challenge") == "" || !strings.Contains(parsed.Query().Get("scope"), "spark:messages_read") {
		t.Fatalf("authorization query = %v", parsed.Query())
	}
	// The first read lists the room's page; the second confirms the committed
	// cursor by reading the tail.
	peer.waitForPages(t, 2)
	stop()
	withStore(func(db *canon.Store) {
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='connected'"); n != 1 {
			t.Fatalf("connected sources = %d", n)
		}
		if n := countRows(t, h.ctx, db, `SELECT count(*) FROM messenger_chat WHERE platform='webex' AND chat='room-1' AND cursor LIKE 'webex/1/%'`); n != 1 {
			var cursor string
			_ = db.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, "SELECT cursor FROM messenger_chat WHERE platform='webex' AND chat='room-1'").Scan(&cursor)
			})
			var state, failure string
			_ = db.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, "SELECT state, failure FROM messenger_source WHERE platform='webex'").Scan(&state, &failure)
			})
			t.Fatalf("the room has no committed cursor: %q source=%s/%s", cursor, state, failure)
		}
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%' AND speaker_kind='external_participant'"); n != 2 {
			t.Fatalf("participant records = %d, want 2", n)
		}
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%' AND speaker_kind='person'"); n != 1 {
			t.Fatalf("the person's own record = %d, want 1", n)
		}
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM messenger_record WHERE platform='webex'"); n != 3 {
			t.Fatalf("messenger records = %d, want 3", n)
		}
	})

	// Stage 2: a rate limit with Retry-After is waited out, not counted as a
	// source failure.
	peer.limitNext()
	stop = start()
	peer.waitForWait(t)
	stop()
	withStore(func(db *canon.Store) {
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='connected' AND failure=''"); n != 1 {
			t.Fatal("the rate limit turned the source into a failure")
		}
	})

	// Stage 3: the platform revokes the refresh token. The source turns needs
	// sign-in and keeps its records.
	peer.revoke()
	stop = start()
	peer.waitForRefusal(t)
	stop()
	withStore(func(db *canon.Store) {
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='needs_sign_in'"); n != 1 {
			t.Fatal("the revoked token did not mark the source needs sign-in")
		}
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n < 3 {
			t.Fatalf("records = %d after the source lost its credentials", n)
		}
	})
	vault, err := secrets.OpenFiles(filepath.Join(h.root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.GetTokens(webex.TokenKey("person:webex-host")); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("revoked tokens remain: %v", err)
	}
	vault.Close()

	// Stage 4: listing reports the source and its state; disconnect stops it
	// and deletes the credentials.
	stop = start()
	finish := runMessengerClient(t, h.socket, "finish")
	if len(finish.Sources) != 1 || finish.Sources[0].State != "needs_sign_in" {
		t.Fatalf("listed sources = %+v", finish.Sources)
	}
	if finish.Disconnected == nil || *finish.Disconnected != "disconnected" {
		t.Fatalf("disconnect = %v", finish.Disconnected)
	}
	stop()
	withStore(func(db *canon.Store) {
		if n := countRows(t, h.ctx, db, "SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='disconnected'"); n != 1 {
			t.Fatal("the source was not disconnected")
		}
	})
}

// waitForSocket waits until the in-process Core has bound its client socket.
func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Core never bound its client socket")
}

// syntheticWebex is a recorded-response Webex: the token endpoint, the person's
// profile, one room and two pages of its messages, with switches for the rate
// limit and for a revoked refresh token.
type syntheticWebex struct {
	server   *httptest.Server
	mu       sync.Mutex
	pages    int
	limited  bool
	revoked  bool
	refusals int
	waitedC  chan struct{}
}

func newSyntheticWebex(t *testing.T) *syntheticWebex {
	t.Helper()
	peer := &syntheticWebex{waitedC: make(chan struct{}, 4)}
	peer.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/authorize":
			query := r.URL.Query()
			redirect := query.Get("redirect_uri")
			if query.Get("response_type") != "code" || query.Get("client_id") != "synthetic-client" || redirect == "" {
				http.Error(w, "bad authorize", http.StatusBadRequest)
				return
			}
			target, err := url.Parse(redirect)
			if err != nil {
				http.Error(w, "bad redirect", http.StatusBadRequest)
				return
			}
			next := target.Query()
			next.Set("code", "synthetic-code")
			next.Set("state", query.Get("state"))
			target.RawQuery = next.Encode()
			http.Redirect(w, r, target.String(), http.StatusFound)
		case "/v1/access_token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			peer.mu.Lock()
			revoked := peer.revoked && r.PostForm.Get("grant_type") == "refresh_token"
			peer.mu.Unlock()
			if revoked {
				peer.mu.Lock()
				peer.refusals++
				peer.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"The refresh token is invalid or expired."}`))
				return
			}
			if r.PostForm.Get("client_id") != "synthetic-client" || r.PostForm.Get("client_secret") != "synthetic-secret" {
				http.Error(w, "bad client", http.StatusBadRequest)
				return
			}
			refresh := "synthetic-refresh"
			if r.PostForm.Get("grant_type") == "refresh_token" {
				// The platform rotates the refresh token on each exchange.
				switch r.PostForm.Get("refresh_token") {
				case "synthetic-refresh", "synthetic-refresh-2":
					refresh = "synthetic-refresh-2"
				default:
					http.Error(w, "bad refresh", http.StatusBadRequest)
					return
				}
			}
			// A short-lived access token, so the scenario reaches the refresh
			// before the source's next read.
			json.NewEncoder(w).Encode(map[string]any{"access_token": "synthetic-access", "expires_in": 1, "refresh_token": refresh, "token_type": "Bearer"})
		case "/v1/people/me":
			w.Write([]byte(`{"id":"person-1","displayName":"Jun","emails":["jun@example.com"]}`))
		case "/v1/rooms":
			peer.mu.Lock()
			limited := peer.limited
			if limited {
				peer.limited = false
			}
			peer.mu.Unlock()
			if limited {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"message":"rate limited"}`))
				select {
				case peer.waitedC <- struct{}{}:
				default:
				}
				return
			}
			w.Write([]byte(`{"items":[{"id":"room-1","title":"Project Unicorn","type":"group"}]}`))
		case "/v1/messages":
			if r.URL.Query().Get("roomId") != "room-1" {
				http.Error(w, "bad room", http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("beforeMessage") == "" {
				// Newest first, as Webex returns a room's messages.
				w.Write([]byte(`{"items":[{"id":"m-3","roomId":"room-1","text":"third","personId":"person-1","personEmail":"jun@example.com","created":"2026-10-20T12:03:00.000Z"},{"id":"m-2","roomId":"room-1","text":"second","personId":"person-2","personEmail":"mira@example.com","created":"2026-10-20T12:02:00.000Z"},{"id":"m-1","roomId":"room-1","text":"first","personId":"person-2","personEmail":"mira@example.com","created":"2026-10-20T12:01:00.000Z"}]}`))
				peer.mu.Lock()
				peer.pages++
				peer.mu.Unlock()
				return
			}
			w.Write([]byte(`{"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(peer.server.Close)
	return peer
}

func (p *syntheticWebex) limitNext() {
	p.mu.Lock()
	p.limited = true
	p.mu.Unlock()
}

func (p *syntheticWebex) revoke() {
	p.mu.Lock()
	p.revoked = true
	p.mu.Unlock()
}

// waitForWait waits until Core has been told to wait out a rate limit.
func (p *syntheticWebex) waitForWait(t *testing.T) {
	t.Helper()
	select {
	case <-p.waitedC:
	case <-time.After(operationTimeout):
		t.Fatal("Core never waited out the rate limit")
	}
}

// waitForRefusal waits until the platform refused a refresh.
func (p *syntheticWebex) waitForRefusal(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		refused := p.refusals
		p.mu.Unlock()
		if refused > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Core never tried the revoked refresh token")
}

// waitForPages waits until Core has read the room's first page want times.
func (p *syntheticWebex) waitForPages(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		pages := p.pages
		p.mu.Unlock()
		if pages >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Core never read the room")
}

func countRows(t *testing.T, ctx context.Context, db *canon.Store, query string) int {
	t.Helper()
	var n int
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, query).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitFor polls a condition until the scenario deadline.
func waitFor(t *testing.T, ctx context.Context, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not reached before the deadline")
}
