// SPDX-License-Identifier: Apache-2.0
package teams

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func service(t *testing.T, transport http.RoundTripper) *Service {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	s := NewService(t.Context(), db, vault, messenger.Open(db, nil, nil), "owner", transport)
	t.Cleanup(s.Close)
	return s
}
func TestOAuthPKCEBindingPersistenceAndDisconnect(t *testing.T) {
	var challenge string
	exchanges := 0
	s := service(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "login.microsoftonline.com" {
			exchanges++
			if r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" || r.Method != "POST" {
				t.Fatal("bad token destination", r.URL)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("client_id") != clientID || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Header.Get("Authorization") != "" {
				t.Fatal("PKCE/client binding lost")
			}
			return response(200, `{"access_token":"access-fixture","refresh_token":"refresh-fixture","token_type":"Bearer","expires_in":3600,"scope":"`+strings.Join(scopes, " ")+`"}`), nil
		}
		if r.URL.Host != "graph.microsoft.com" || r.URL.Path != "/v1.0/me" {
			t.Fatal("unexpected destination", r.URL)
		}
		return response(200, `{"id":"user"}`), nil
	}))
	// Keep this an OAuth/storage test; running Core acceptance exercises workers.
	s.run = false
	c := Connection{ID: "work", Tenant: tenant, ClientID: clientID}
	if err := s.Configure(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	target, err := s.Begin(t.Context(), "work", "session", 32123)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	q := u.Query()
	challenge = q.Get("code_challenge")
	if u.Host != "login.microsoftonline.com" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "http://localhost:32123/auth/callback" {
		t.Fatal("invalid authorize URL", u)
	}
	if err = s.Complete(t.Context(), "other", "code", q.Get("state"), "", ""); err == nil || exchanges != 0 {
		t.Fatal("accepted wrong connection")
	}
	if err = s.CompleteURL(t.Context(), "session", "http://127.0.0.1:32123/auth/callback?"+url.Values{"code": {"code"}, "state": {q.Get("state")}, "iss": {login + tenant + "/v2.0"}}.Encode()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Connections(t.Context())
	if err != nil || len(rows) != 1 || rows[0].Account != tenant+":user" || rows[0].Status != "connected" {
		t.Fatalf("status %+v %v", rows, err)
	}
	if err = s.Complete(t.Context(), "session", "code", q.Get("state"), "", ""); err == nil || exchanges != 1 {
		t.Fatal("replayed exchange")
	}
	if err = s.Disconnect(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.vault.GetTokens(s.key(c)); err != secrets.ErrNotFound {
		t.Fatal("credentials survived", err)
	}
	sources, err := s.store.Sources(t.Context(), "owner")
	if err != nil || len(sources) != 1 || sources[0].State != messenger.StateDisconnected {
		t.Fatal(sources, err)
	}
}
func TestOAuthRefusesIssuerAndRecordsConsentDenial(t *testing.T) {
	s := service(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("must not exchange"); return nil, nil }))
	s.run = false
	if err := s.Configure(t.Context(), Connection{ID: "work", Tenant: tenant, ClientID: clientID}); err != nil {
		t.Fatal(err)
	}
	target, _ := s.Begin(t.Context(), "work", "session", 32123)
	u, _ := url.Parse(target)
	if err := s.Complete(t.Context(), "session", "code", u.Query().Get("state"), "https://evil.example", ""); err == nil {
		t.Fatal("issuer accepted")
	}
	target, _ = s.Begin(t.Context(), "work", "session", 32123)
	u, _ = url.Parse(target)
	if err := s.Complete(t.Context(), "session", "", u.Query().Get("state"), "", "access_denied"); err != ErrUnsupported {
		t.Fatal(err)
	}
	rows, _ := s.Connections(t.Context())
	if rows[0].Status != "unsupported" || rows[0].Reason == "" {
		t.Fatal(rows)
	}
}

func TestRefreshRotatesTokensAndKeepsMissingReplacementRefresh(t *testing.T) {
	var seenRefresh string
	s := service(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "login.microsoftonline.com" || r.URL.Path != "/"+tenant+"/oauth2/v2.0/token" {
			t.Fatal("refresh destination", r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		seenRefresh = r.Form.Get("refresh_token")
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != clientID || r.Header.Get("X-Refresh") != "" {
			t.Fatal("refresh boundary")
		}
		return response(200, `{"access_token":"rotated-access","token_type":"Bearer","expires_in":3600,"scope":"`+strings.Join(scopes, " ")+`"}`), nil
	}))
	s.run = false
	c := Connection{ID: "work", Tenant: tenant, ClientID: clientID, Account: tenant + ":owner", Status: "connected"}
	if err := s.save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.vault.PutTokens(s.key(c), secrets.NewTokens("old-access", "old-refresh")); err != nil {
		t.Fatal(err)
	}
	token, err := s.token(t.Context(), c.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	header := make(http.Header)
	if err = secret.InjectHeader(header, "Authorization", "Bearer ", token); err != nil {
		t.Fatal(err)
	}
	if header.Get("Authorization") != "Bearer rotated-access" || seenRefresh != "old-refresh" {
		t.Fatal("rotation failed")
	}
	stored, err := s.vault.GetTokens(s.key(c))
	if err != nil {
		t.Fatal(err)
	}
	if err = secret.InjectHeader(header, "Refresh", "", stored.Refresh()); err != nil || header.Get("Refresh") != "old-refresh" {
		t.Fatal("refresh token lost")
	}
}
func TestConfigurationAndCallbackBoundaries(t *testing.T) {
	s := service(t, transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("must not exchange"); return nil, nil }))
	s.run = false
	for _, c := range []Connection{{ID: "work", Tenant: "consumers", ClientID: clientID}, {ID: "work", Tenant: "9188040d-6c67-4c5b-b112-36a304b66dad", ClientID: clientID}, {ID: "work", Tenant: tenant, ClientID: "1fec8e78-bce4-4aaf-ab1b-5451cc387264"}, {ID: "../work", Tenant: tenant, ClientID: clientID}} {
		if err := s.Configure(t.Context(), c); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	if err := s.Configure(t.Context(), Connection{ID: "work", Tenant: tenant, ClientID: clientID}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"http://evil.example/auth/callback?code=x&state=state", "http://localhost:32123/auth/callback?code=x&state=x&state=y", "http://localhost:32124/auth/callback?code=x&state=x"} {
		if _, err := s.Begin(t.Context(), "work", "session", 32123); err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteURL(t.Context(), "session", bad); err == nil {
			t.Fatal("callback accepted", bad)
		}
	}
	target, _ := s.Begin(t.Context(), "work", "session", 32123)
	u, _ := url.Parse(target)
	if err := s.Disconnect(t.Context(), "work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(t.Context(), "session", "code", u.Query().Get("state"), "", ""); err == nil {
		t.Fatal("disconnect failed to invalidate pending exchange")
	}
}

func TestDisconnectCancelsInFlightGraphRead(t *testing.T) {
	entered := make(chan struct{})
	s := service(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1.0/me/chats" {
			t.Error("unexpected endpoint", r.URL)
		}
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	c := Connection{ID: "work", Tenant: tenant, ClientID: clientID, Account: tenant + ":owner", Status: "connected", Expires: time.Now().Add(time.Hour)}
	if err := s.save(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Connect(t.Context(), s.source(c)); err != nil {
		t.Fatal(err)
	}
	if err := s.vault.PutTokens(s.key(c), secrets.NewTokens("access", "refresh")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sync never entered Graph")
	}
	if err := s.Disconnect(t.Context(), c.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Connections(t.Context())
	if err != nil || rows[0].Status != "disconnected" {
		t.Fatal(rows, err)
	}
	sources, err := s.store.Sources(t.Context(), s.person)
	if err != nil || sources[0].State != messenger.StateDisconnected {
		t.Fatal(sources, err)
	}
	if _, err = s.vault.GetTokens(s.key(c)); err != secrets.ErrNotFound {
		t.Fatal("disconnect left credentials", err)
	}
	if err = s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
}
