// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

type teamsTransport func(*http.Request) (*http.Response, error)

func (f teamsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestM3TeamsGraphInstalledCore(t *testing.T) {
	t.Parallel()
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
		if err = os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"teams-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, closeDB := installedAcceptanceStore(t, h)
	closeDB()
	const tenant = "11111111-1111-4111-8111-111111111111"
	const app = "22222222-2222-4222-8222-222222222222"
	var round atomic.Int32
	var requests atomic.Int32
	var tokenRequests atomic.Int32
	var denied atomic.Bool
	var throttled atomic.Bool
	wire := func(code int, body string) *http.Response {
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	}
	transport := teamsTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Scheme != "https" || r.URL.Host != "graph.microsoft.com" && r.URL.Host != "login.microsoftonline.com" {
			t.Errorf("forbidden Core destination %s", r.URL)
			return nil, fmt.Errorf("forbidden destination")
		}
		if r.URL.Host == "login.microsoftonline.com" {
			tokenRequests.Add(1)
			if r.Method != "POST" || r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" {
				t.Error("unexpected sign-in endpoint", r.URL)
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("client_id") != app || len(r.Form.Get("code_verifier")) < 43 {
				t.Error("own app/PKCE missing")
			}
			if denied.Load() {
				return wire(400, `{"error":"invalid_grant","error_codes":[65001],"error_description":"synthetic private diagnostic"}`), nil
			}
			body, _ := json.Marshal(map[string]any{"access_token": "teams-access-fixture", "refresh_token": "teams-refresh-fixture", "token_type": "Bearer", "expires_in": 3600, "scope": r.Form.Get("scope")})
			return wire(200, string(body)), nil
		}
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer teams-access-fixture" {
			t.Error("non-read request or missing token")
		}
		switch r.URL.Path {
		case "/v1.0/me":
			return wire(200, `{"id":"owner"}`), nil
		case "/v1.0/me/chats":
			if r.URL.Query().Get("$skiptoken") == "next" {
				return wire(200, `{"value":[]}`), nil
			}
			return wire(200, `{"value":[{"id":"chat","topic":"Fixture","chatType":"oneOnOne"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/chats?$skiptoken=next"}`), nil
		case "/v1.0/me/joinedTeams":
			return wire(200, `{"value":[{"id":"team","displayName":"Team"}]}`), nil
		case "/v1.0/teams/team/channels":
			return wire(200, `{"value":[{"id":"channel","displayName":"Channel"}]}`), nil
		case "/v1.0/teams/team/channels/channel/messages":
			return wire(200, `{"value":[{"id":"root","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-20T10:00:00Z","messageType":"systemEventMessage","body":{"content":"channel notice"}}]}`), nil
		case "/v1.0/teams/team/channels/channel/messages/root/replies":
			return wire(200, `{"value":[{"id":"reply","replyToId":"root","createdDateTime":"2026-10-21T10:00:00Z","lastModifiedDateTime":"2026-10-21T10:00:00Z","from":{"application":{"id":"app","displayName":"App"}},"body":{"content":"channel reply"}}]}`), nil
		case "/v1.0/users/owner":
			return wire(200, `{"id":"owner","displayName":"Owner","mail":"owner@example.test"}`), nil
		case "/v1.0/chats/chat/messages":
			if round.Load() == 1 {
				if !strings.Contains(r.URL.Query().Get("$filter"), "lastModifiedDateTime gt") {
					t.Error("restart lost timestamp filter")
				}
				if !throttled.Swap(true) {
					res := wire(429, `{"error":{"code":"TooManyRequests"}}`)
					res.Header.Set("Retry-After", "0")
					return res, nil
				}
				return wire(200, `{"value":[{"id":"m1","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-23T12:00:00Z","from":{"user":{"id":"owner","tenantId":"`+tenant+`"}},"body":{"content":"Teams revised"}},{"id":"m2","lastModifiedDateTime":"2026-10-23T12:00:00Z","deletedDateTime":"2026-10-23T12:00:00Z"}]}`), nil
			}
			if r.URL.Query().Get("$skiptoken") == "next" {
				return wire(200, `{"value":[{"id":"m1","createdDateTime":"2026-10-20T10:00:00Z","lastModifiedDateTime":"2026-10-20T10:00:00Z","from":{"user":{"id":"owner","tenantId":"`+tenant+`"}},"body":{"content":"Teams original"}}]}`), nil
			}
			return wire(200, `{"value":[{"id":"m2","createdDateTime":"2026-10-21T10:00:00Z","lastModifiedDateTime":"2026-10-21T10:00:00Z","from":{"application":{"id":"bot"}},"body":{"content":"Teams delete"}}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/chats/chat/messages?$skiptoken=next"}`), nil
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			return nil, fmt.Errorf("unexpected endpoint")
		}
	})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original, err := url.Parse(r.Header.Get("X-Teams-Fixture-URL"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		r.URL = original
		r.RequestURI = ""
		r.Header.Del("X-Teams-Fixture-URL")
		res, err := transport.RoundTrip(r)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		defer res.Body.Close()
		for key, values := range res.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	defer peer.Close()
	start := func() (*clientapi.Client, func()) {
		prior := h.binary
		h.binary = executable
		child := h.command("-test.run=^TestTeamsBootHelper$")
		h.binary = prior
		child.command.Env = append(child.command.Env, "LINA_TEST_TEAMS_ROOT="+h.root, "LINA_TEST_TEAMS_SOCKET="+h.socket, "LINA_TEST_TEAMS_HTTP="+peer.URL)
		child.start(t)
		var once sync.Once
		stop := func() { once.Do(func() { h.stopCore(child) }) }
		t.Cleanup(stop)
		deadline, stopWait := context.WithTimeout(h.ctx, operationTimeout)
		defer stopWait()
		for {
			c, err := clientapi.DialWithCapabilities(deadline, h.socket, "test", []protocol.Capability{{Name: "teams", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}})
			if err == nil {
				t.Cleanup(func() { c.Close() })
				// Socket availability precedes recovery admission. Probe the
				// read-only list operation until Core admits settings.
				for {
					id, err := c.Teams(deadline, protocol.TeamsRequest{Operation: "list"})
					if err != nil {
						t.Fatal(err)
					}
					e, err := c.Receive(deadline)
					if err != nil {
						t.Fatal(err)
					}
					if e.ReplyTo == nil || *e.ReplyTo != id {
						t.Fatal("uncorrelated readiness result")
					}
					if e.Kind == protocol.KindTeamsResult {
						return c, stop
					}
					if e.Error == nil || e.Error.Code != protocol.ErrorUnavailable {
						t.Fatalf("settings readiness: %+v", e.Error)
					}
					select {
					case <-deadline.Done():
						t.Fatal("settings never admitted")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			select {
			case <-deadline.Done():
				t.Fatal("Core connection", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	c, stop := start()
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	count := func(query string, want int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			var n int
			err := reader.Read(ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(ctx, query).Scan(&n) })
			if err != nil {
				t.Fatal(err)
			}
			if n == want {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("count=%d want=%d: %s", n, want, query)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	receive := func(id string, err error) protocol.Envelope {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			e, err := c.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo != nil && *e.ReplyTo == id {
				if e.Kind == protocol.KindError {
					t.Fatalf("request refused: %+v", e.Error)
				}
				return e
			}
		}
	}
	id, tid, cid := "work", tenant, app
	configure := h.command("teams", "--socket", h.socket)
	configure.command.Stdin = strings.NewReader(`{"operation":"configure","id":"work","tenant":"` + tenant + `","client_id":"` + app + `"}`)
	configure.start(t)
	configure.wait(t, 0)
	var configured protocol.TeamsResult
	if json.Unmarshal(configure.stdout.Bytes(), &configured) != nil || configured.Connections == nil || len(*configured.Connections) != 1 {
		t.Fatal("CLI did not configure Teams")
	}
	signIn := func(id string) protocol.TeamsResult {
		t.Helper()
		e := receive(c.Teams(h.ctx, protocol.TeamsRequest{Operation: "sign_in", ID: &id}))
		if e.Kind != protocol.KindSignInListen {
			t.Fatal(e.Kind)
		}
		reason := "remote terminal"
		e = receive(c.SignInListening(h.ctx, protocol.SignInListening{CannotListen: &reason}))
		opened := e.Payload.(protocol.SignInOpen)
		u, err := url.Parse(*opened.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != "login.microsoftonline.com" || u.Query().Get("client_id") != app || u.Query().Get("code_challenge_method") != "S256" {
			t.Fatal("invalid authorization request")
		}
		callback, _ := url.Parse(u.Query().Get("redirect_uri"))
		q := url.Values{"code": {"synthetic-code"}, "state": {u.Query().Get("state")}}
		callback.RawQuery = q.Encode()
		target := callback.String()
		e = receive(c.SignInRedirect(h.ctx, protocol.SignInRedirect{URL: &target}))
		return e.Payload.(protocol.TeamsResult)
	}
	result := signIn(id)
	if result.Connections == nil || (*result.Connections)[0].Status != "connected" {
		t.Fatal(result)
	}
	count(`SELECT count(*) FROM messenger_coverage`, 2)
	count(`SELECT count(*) FROM conv_event WHERE body LIKE 'Teams %'`, 2)
	count(`SELECT count(*) FROM messenger_author WHERE author='`+tenant+`:owner' AND email='owner@example.test'`, 1)
	c.Close()
	stop()
	round.Store(1)
	c, stop = start()
	count(`SELECT count(*) FROM messenger_record WHERE message='m1' AND revision=2`, 1)
	count(`SELECT count(*) FROM messenger_record WHERE message='m2' AND state='deleted'`, 1)
	count(`SELECT count(*) FROM retract_visible_event WHERE body='Teams delete'`, 0)
	count(`SELECT count(*) FROM ev_rel WHERE kind='supersedes'`, 1)
	count(`SELECT count(*) FROM conv_event WHERE body='channel reply'`, 1)
	if tokenRequests.Load() != 1 {
		t.Fatal("restart did not reuse stored credentials")
	}
	receive(c.Teams(h.ctx, protocol.TeamsRequest{Operation: "disconnect", ID: &id}))
	count(`SELECT count(*) FROM messenger_source WHERE state='disconnected'`, 1)
	denied.Store(true)
	id = "blocked"
	receive(c.Teams(h.ctx, protocol.TeamsRequest{Operation: "configure", ID: &id, Tenant: &tid, ClientID: &cid}))
	result = signIn(id)
	found := false
	for _, row := range *result.Connections {
		if row.ID == id {
			found = row.Status == "unsupported" && row.Reason != "" && !strings.Contains(row.Reason, "private")
		}
	}
	if !found {
		t.Fatal("missing unsupported reason", result)
	}
	count(`SELECT count(*) FROM teams_connection WHERE CAST(record AS TEXT) LIKE '%teams-access-fixture%' OR CAST(record AS TEXT) LIKE '%teams-refresh-fixture%'`, 0)
	c.Close()
	stop()
	before := requests.Load()
	c, stop = start()
	receive(c.Teams(h.ctx, protocol.TeamsRequest{Operation: "list"}))
	c.Close()
	stop()
	if requests.Load() != before {
		t.Fatal("disconnected/unsupported connection made network requests after restart")
	}
}

// Core is a child so the parent client passes the real socket decider boundary.
func TestTeamsBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_TEAMS_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_TEAMS_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	transport := teamsTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "graph.microsoft.com" && req.URL.Host != "login.microsoftonline.com" {
			return nil, fmt.Errorf("forbidden fixture destination")
		}
		wire := req.Clone(req.Context())
		wire.Header.Set("X-Teams-Fixture-URL", req.URL.String())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_TEAMS_SOCKET"), ChannelTransport: transport}); err != nil {
		t.Fatal(err)
	}
}
