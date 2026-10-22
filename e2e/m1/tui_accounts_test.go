// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type observedSignIn struct {
	*accounts.SignIn
	opened    chan accounts.Pending
	completed chan accounts.Account
}

func (s *observedSignIn) Begin(ctx context.Context, p provider.ProviderID, caps accounts.RelayCaps) (accounts.Pending, error) {
	pending, err := s.SignIn.Begin(ctx, p, caps)
	if err == nil {
		s.opened <- pending
	}
	return pending, err
}
func (s *observedSignIn) Complete(ctx context.Context, id string, input accounts.Relayed) (accounts.Account, error) {
	account, err := s.SignIn.Complete(ctx, id, input)
	if err == nil {
		s.completed <- account
	}
	return account, err
}

func TestM1TUIAccounts(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("tui-accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Person, Subject string
		Callbacks       []string
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	for _, callbackMode := range scenario.Callbacks {
		t.Run(callbackMode, func(t *testing.T) {
			h := newHarness(t, binary)
			controls, err := ctlfiles.Open(h.root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { controls.Close() })
			if err := controls.Acquire(); err != nil {
				t.Fatal(err)
			}
			migrations := canon.Migrations()
			db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: migrations})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close(context.Background()) })
			vault, err := secrets.OpenFiles(h.root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { vault.Close() })
			store, err := accounts.Open(h.ctx, db, vault, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			wires := &signInWires{key: key, grants: make(map[string]map[string]any)}
			service, err := accounts.NewSignIn(h.ctx, accounts.SignInConfig{Store: store, Person: scenario.Person, Leaser: signInLease{}, Transport: wires})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(service.Close)
			observed := &observedSignIn{SignIn: service, opened: make(chan accounts.Pending, 1), completed: make(chan accounts.Account, 1)}
			relay := clientsrv.NewSignInRelay(scenario.Person, observed)
			rows := clientsrv.LocalCombinations()
			for i := range rows {
				rows[i].Capabilities = []protocol.Capability{clientsrv.AccountsCapability, clientsrv.SignInCapability}
				rows[i].Evidence = []string{"go test ./e2e/m1 -run TestM1TUIAccounts"}
			}
			listener, err := clientsrv.Listen(h.socket)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(h.ctx)
			done := make(chan error, 1)
			go func() {
				done <- clientsrv.Serve(ctx, listener, clientsrv.Identity{MainID: "main-e2e", Epoch: "1", PersonID: scenario.Person, Operations: clientsrv.AccountOperations(scenario.Person, store, relay), Supported: rows})
			}()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			// Never launch a real browser. The fixture launcher gets a single argv URL.
			bin := filepath.Join(h.root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"xdg-open", "open"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"TERM=xterm-256color", "PATH=" + bin}
			if callbackMode == "pasted" {
				env = append(env, "SSH_CONNECTION=synthetic")
			}
			terminal := h.startScreenTUI(env...)
			waitScreen(t, h, terminal, "/signin")
			h.keys(terminal, "/signin\r")
			var pending accounts.Pending
			select {
			case pending = <-observed.opened:
			case <-h.ctx.Done():
				t.Fatal("TUI did not begin sign-in")
			}
			waitScreen(t, h, terminal, design.Text(design.English, "sign_in_open"))
			authorization, err := url.Parse(pending.URL)
			if err != nil {
				t.Fatal(err)
			}
			query := authorization.Query()
			code, clientID := "synthetic-tui-code", "oaiapp_synthetic"
			wires.mu.Lock()
			wires.grants[code] = map[string]any{"iss": "https://auth.openai.com", "aud": clientID, "sub": scenario.Subject, "nonce": query.Get("nonce"), "iat": time.Now().Unix() - 1, "exp": time.Now().Add(time.Hour).Unix()}
			wires.mu.Unlock()
			callback := query.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {query.Get("state")}, "iss": {"https://auth.openai.com"}, "client_id": {clientID}}.Encode()
			if callbackMode == "pasted" {
				h.keys(terminal, "\x1b[200~"+callback+"\x1b[201~\r")
			} else {
				request, _ := http.NewRequestWithContext(h.ctx, http.MethodGet, callback, nil)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatal("loopback refused callback")
				}
			}
			var saved accounts.Account
			select {
			case saved = <-observed.completed:
			case <-h.ctx.Done():
				t.Fatal("TUI did not complete sign-in")
			}
			view := waitScreen(t, h, terminal, design.Text(design.English, "ok"))
			if strings.Contains(screenText(view), code) {
				t.Fatal("callback code leaked to cells")
			}
			account, err := store.Get(h.ctx, saved.ID)
			if err != nil || account.Status != accounts.OK {
				t.Fatal("sign-in was not durable")
			}
			binding, err := accounts.NewOwnerBindings(db).Get(h.ctx)
			if err != nil || binding.Subject != scenario.Subject {
				t.Fatal("owner binding missing")
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
		})
	}
}
