// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

type webexTransport func(*http.Request) (*http.Response, error)

func (f webexTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A supervised child runs real Core with only its outbound HTTP transport
// replaced. The parent is an ordinary client, outside Core's process tree.
func TestM3WebexSourceInstalledCore(t *testing.T) {
	t.Parallel()
	peer := newSyntheticWebex(t)
	h := newHarness(t, buildLina(t))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	manifest, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"webex-host"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, closeDB := installedAcceptanceStore(t, h)
	closeDB()
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	count := func(query string, want int) {
		t.Helper()
		deadline, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			var got int
			err := reader.Read(deadline, func(tx *canon.Tx) error { return tx.QueryRowContext(deadline, query).Scan(&got) })
			if err == nil && got == want {
				return
			}
			select {
			case <-deadline.Done():
				t.Fatalf("%s: got %d, want %d (%v)", query, got, want, err)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	start := func() func() {
		prior := h.binary
		h.binary = executable
		child := h.command("-test.run=^TestWebexBootHelper$")
		h.binary = prior
		child.command.Env = append(child.command.Env, "LINA_TEST_WEBEX_ROOT="+h.root, "LINA_TEST_WEBEX_SOCKET="+h.socket, "LINA_TEST_WEBEX_HTTP="+peer.server.URL)
		child.start(t)
		var once sync.Once
		stop := func() { once.Do(func() { h.stopCore(child) }) }
		t.Cleanup(stop)
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			c, err := clientapi.DialWithCapabilities(ctx, h.socket, "test", []protocol.Capability{{Name: "webex", Version: "1.0"}})
			if err == nil {
				_, err = c.Webex(ctx, protocol.WebexRequest{Operation: new("list")})
				if err == nil {
					e, readErr := c.Receive(ctx)
					c.Close()
					if readErr == nil && e.Error != nil && e.Error.Code != protocol.ErrorUnavailable {
						t.Fatalf("readiness rejected: %+v", e.Error)
					}
					if readErr != nil {
						t.Fatal(readErr)
					}
					if readErr == nil && e.Kind == protocol.KindWebexResult {
						return stop
					}
				} else {
					c.Close()
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal("Core did not become ready", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	stop := start()
	list := h.command("webex", "--socket", h.socket)
	list.command.Stdin = strings.NewReader(`{"operation":"list"}`)
	list.start(t)
	list.wait(t, 0)
	var listed protocol.WebexResult
	if err := json.Unmarshal(list.stdout.Bytes(), &listed); err != nil || listed.Sources == nil || len(*listed.Sources) != 0 {
		t.Fatalf("CLI list: %v", err)
	}
	setup, err := webexClientRun(h.ctx, h.socket, "setup", peer.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Status != "connected" || setup.Platform != "webex" {
		t.Fatalf("sign-in: %+v", setup)
	}
	parsed, err := url.Parse(setup.Authorize)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "webexapis.com" || parsed.Query().Get("code_challenge_method") != "S256" || !strings.Contains(parsed.Query().Get("scope"), "spark:messages_read") {
		t.Fatal("invalid authorization request")
	}
	count("SELECT count(*) FROM messenger_record WHERE platform='webex'", 3)
	count("SELECT count(*) FROM messenger_chat WHERE platform='webex' AND cursor LIKE 'webex/1/tail/%'", 1)
	count("SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%' AND speaker_kind='person'", 1)
	count("SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%' AND speaker_kind='external_participant'", 2)
	stop()
	peer.mu.Lock()
	refreshed := peer.refreshes
	peer.mu.Unlock()
	if refreshed == 0 {
		t.Fatal("Core never refreshed its token")
	}
	// Restart uses the durable tail, honors 429, and admits no duplicate records.
	peer.limitNext()
	stop = start()
	select {
	case <-peer.waitedC:
	case <-time.After(operationTimeout):
		t.Fatal("no rate limit observed")
	}
	count("SELECT count(*) FROM messenger_coverage WHERE platform='webex'", 2)
	count("SELECT count(*) FROM messenger_record WHERE platform='webex'", 3)
	count("SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='connected' AND failure=''", 1)
	stop()
	peer.revoke()
	stop = start()
	count("SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='needs_sign_in'", 1)
	count("SELECT count(*) FROM messenger_record WHERE platform='webex'", 3)
	finish, err := webexClientRun(h.ctx, h.socket, "finish", peer.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(finish.Sources) != 1 || finish.Sources[0].State != "needs_sign_in" || finish.Disconnected == nil || *finish.Disconnected != "disconnected" {
		t.Fatalf("disconnect: %+v", finish)
	}
	count("SELECT count(*) FROM messenger_source WHERE platform='webex' AND state='disconnected'", 1)
	stop()
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	for _, key := range []secrets.Key{webex.TokenKey("person:webex-host"), webex.ClientKey("person:webex-host")} {
		if _, err := vault.Get(key); !errors.Is(err, secrets.ErrNotFound) {
			t.Fatalf("credential retained after disconnect: %v", err)
		}
	}
}

func TestWebexBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_WEBEX_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_WEBEX_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	transport := webexTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "webexapis.com" {
			return nil, fmt.Errorf("unexpected fixture destination")
		}
		wire := r.Clone(r.Context())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_WEBEX_SOCKET"), ChannelTransport: transport}); err != nil {
		t.Fatal(err)
	}
}

// syntheticWebex is a recorded-response Webex: the token endpoint, the person's
// profile, one room and two pages of its messages, with switches for the rate
// limit and for a revoked refresh token.
type syntheticWebex struct {
	server    *httptest.Server
	mu        sync.Mutex
	pages     int
	limited   bool
	revoked   bool
	refusals  int
	waitedC   chan struct{}
	refreshes int
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
				peer.mu.Lock()
				peer.refreshes++
				peer.mu.Unlock()
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
				w.Header().Set("Link", `<https://webexapis.com/v1/messages?roomId=room-1&beforeMessage=m-2>; rel="next"`)
				w.Write([]byte(`{"items":[{"id":"m-3","roomId":"room-1","text":"third","personId":"person-1","personEmail":"owner@example.test","created":"2026-10-20T12:03:00Z"},{"id":"m-2","roomId":"room-1","text":"second","personId":"person-2","created":"2026-10-20T12:02:00Z"}]}`))
			} else if r.URL.Query().Get("beforeMessage") == "m-2" {
				w.Write([]byte(`{"items":[{"id":"m-1","roomId":"room-1","text":"first","personId":"person-2","created":"2026-10-20T12:01:00Z"}]}`))
			} else {
				http.Error(w, "unexpected cursor", 400)
			}
			peer.mu.Lock()
			peer.pages++
			peer.mu.Unlock()
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
