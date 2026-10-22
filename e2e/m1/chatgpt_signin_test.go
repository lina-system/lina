// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

type signInLease struct{}

func (signInLease) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

type signInWires struct {
	mu     sync.Mutex
	key    *rsa.PrivateKey
	grants map[string]map[string]any
}

func (w *signInWires) RoundTrip(r *http.Request) (*http.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var body any
	switch r.URL.String() {
	case "https://auth.openai.com/.well-known/jwks.json":
		body = map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "synthetic", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(w.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(w.key.E)).Bytes())}}}
	case "https://auth.openai.com/api/accounts/oauth/token":
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		claims := w.grants[r.Form.Get("code")]
		if claims == nil || claims["aud"] != r.Form.Get("client_id") {
			return nil, fmt.Errorf("unrecorded code exchange")
		}
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "synthetic"})
		payload, _ := json.Marshal(claims)
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		digest := sha256.Sum256([]byte(input))
		sig, err := rsa.SignPKCS1v15(rand.Reader, w.key, crypto.SHA256, digest[:])
		if err != nil {
			return nil, err
		}
		body = map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct", "id_token": input + "." + base64.RawURLEncoding.EncodeToString(sig)}
	default:
		return nil, fmt.Errorf("unrecorded issuer request")
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

// Reuse #34's isolated state, generated-type socket client, deadlines and
// cleanup. Production sign-in and accounts run behind the real client listener;
// only the remote issuer is replaced by synthetic signed recorded wires.
func TestM1ChatGPTSignInOwnerBinding(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("chatgpt-signin.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Person string `json:"person_id"`
		Steps  []struct {
			Purpose  string `json:"purpose"`
			Subject  string `json:"subject"`
			Callback string `json:"callback"`
			Outcome  string `json:"outcome"`
			Owner    string `json:"owner_subject"`
		} `json:"steps"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
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
	relay := clientsrv.NewSignInRelay(scenario.Person, service)
	caps := []protocol.Capability{clientsrv.AccountsCapability, clientsrv.SignInCapability}
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = caps
		rows[i].Evidence = []string{"go test ./e2e/m1 -run TestM1ChatGPTSignInOwnerBinding"}
	}
	listener, err := clientsrv.Listen(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	serving, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(serving, listener, clientsrv.Identity{MainID: "main-e2e", Epoch: "1", PersonID: scenario.Person, Operations: relay.Operations(), Supported: rows})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	peer, platform := "lina-tui", rows[0].Platform
	answer := c.call(t, protocol.KindHello, protocol.Hello{Party: protocol.PartyClient, ID: "client-e2e", Peer: &peer, Platform: &platform, ProductVersion: "synthetic", ProtocolVersions: []string{"1.0"}, Capabilities: caps})
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || len(welcome.Capabilities) != 2 || welcome.PersonID != scenario.Person {
		t.Fatalf("handshake: %+v", answer.Error)
	}
	c.welcome = welcome
	var firstAccount *string
	for i, step := range scenario.Steps {
		operation, providerID, purpose := "sign_in", "chatgpt", step.Purpose
		request := protocol.AccountRequest{Operation: &operation, Provider: &providerID, Purpose: &purpose}
		if i == len(scenario.Steps)-1 {
			request.AccountID = firstAccount
		}
		answer = c.call(t, protocol.KindAccountRequest, request)
		if answer.Kind != protocol.KindSignInListen {
			t.Fatalf("step %d begin: %+v", i, answer.Error)
		}
		report := protocol.SignInListening{}
		if step.Callback == "pasted" {
			reason := "remote_terminal"
			report.CannotListen = &reason
		} else {
			port := json.Number("32123")
			report.Port = &port
		}
		answer = c.call(t, protocol.KindSignInListening, report)
		open, ok := answer.Payload.(protocol.SignInOpen)
		if !ok || open.URL == nil {
			t.Fatalf("step %d listen: %+v", i, answer.Error)
		}
		authorization, _ := url.Parse(*open.URL)
		query := authorization.Query()
		issued := query.Get("client_id")
		if issued == "dynamic_agent_client" {
			issued = "oaiapp_" + step.Subject
		}
		code := "synthetic-code-" + strconv.Itoa(i)
		wires.mu.Lock()
		wires.grants[code] = map[string]any{"iss": "https://auth.openai.com", "aud": issued, "sub": step.Subject, "nonce": query.Get("nonce"), "iat": time.Now().Unix() - 1, "exp": time.Now().Add(time.Hour).Unix()}
		wires.mu.Unlock()
		callback := query.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {query.Get("state")}, "iss": {"https://auth.openai.com"}, "client_id": {issued}}.Encode()
		redirect := protocol.SignInRedirect{URL: &callback}
		if i == len(scenario.Steps)-1 {
			state, issuer := query.Get("state"), "https://auth.openai.com"
			redirect = protocol.SignInRedirect{Code: &code, State: &state, Iss: &issuer}
		}
		answer = c.call(t, protocol.KindSignInRedirect, redirect)
		if step.Outcome == "applied" {
			result, ok := answer.Payload.(protocol.AccountResult)
			if !ok || result.AccountID == nil || result.Status == nil || *result.Status != "ok" || answer.EffectState != protocol.EffectApplied {
				t.Fatalf("step %d complete: %+v", i, answer.Error)
			}
			if i == 0 {
				firstAccount = result.AccountID
			}
			if i == len(scenario.Steps)-1 && *result.AccountID != *firstAccount {
				t.Fatal("reauthorization replaced the account")
			}
		} else if answer.Error == nil || string(answer.Error.Code) != step.Outcome {
			t.Fatalf("step %d refusal: %+v", i, answer.Error)
		}
		binding, err := accounts.NewOwnerBindings(db).Get(h.ctx)
		if err != nil || binding.Subject != step.Owner || binding.Person != scenario.Person {
			t.Fatalf("step %d changed binding: %+v %v", i, binding, err)
		}
		replay := c.call(t, protocol.KindSignInRedirect, redirect)
		if replay.Error == nil || replay.Error.Code != protocol.ErrorMalformed {
			t.Fatalf("step %d replay accepted", i)
		}
	}
}
