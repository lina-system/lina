// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type signInLeaser struct{}

func (signInLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

type signInWire struct {
	key    *rsa.PrivateKey
	jwks   []byte
	grants map[string]map[string]any
	t      *testing.T
}

func (w *signInWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() == openAIIssuer+"/.well-known/jwks.json" {
		return wireResponse(200, string(w.jwks)), nil
	}
	if r.URL.String() != openAIIssuer+"/api/accounts/oauth/token" || r.ParseForm() != nil {
		return nil, ErrSignInInvalid
	}
	claims := w.grants[r.Form.Get("code")]
	if claims == nil || claims["aud"] != r.Form.Get("client_id") {
		return wireResponse(400, `{"error":"invalid_grant"}`), nil
	}
	raw, _ := json.Marshal(map[string]any{"access_token": "access:" + claims["sub"].(string), "refresh_token": "refresh:" + claims["sub"].(string), "id_token": signFixture(w.t, w.key, claims, "RS256"), "token_type": "Bearer", "scope": chatGPTScopes, "expires_in": 3600})
	return wireResponse(200, string(raw)), nil
}

func signInSetup(t *testing.T) (fixture, *SignIn, *signInWire) {
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
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: SignInSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	store, err := Open(t.Context(), db, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	key, jwks, _ := tokenFixture(t)
	wire := &signInWire{key: key, jwks: jwks, grants: map[string]map[string]any{}, t: t}
	signer, err := NewSignIn(t.Context(), SignInConfig{Store: store, Person: "owner", Leaser: signInLeaser{}, Transport: wire})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(signer.Close)
	return fixture{store, db, vault, root}, signer, wire
}

func completeSignIn(t *testing.T, s *SignIn, w *signInWire, purpose SignInPurpose, subject string, accountID provider.AccountID, paste bool) (Account, error) {
	t.Helper()
	caps := RelayCaps{ConnectionID: "connection-one", Purpose: purpose, AccountID: accountID, CanListen: true, Port: 32123}
	pending, err := s.Begin(t.Context(), provider.ChatGPT, caps)
	if err != nil {
		return Account{}, err
	}
	authorization, _ := url.Parse(pending.URL)
	q := authorization.Query()
	clientID := q.Get("client_id")
	if clientID == registrationClient {
		clientID = "oaiapp_" + subject
	}
	code := randomSignInID()
	w.grants[code] = map[string]any{"iss": openAIIssuer, "sub": subject, "aud": clientID, "nonce": q.Get("nonce"), "iat": time.Now().Unix() - 1, "exp": time.Now().Add(time.Hour).Unix()}
	input := Relayed{ConnectionID: caps.ConnectionID, Code: code, State: q.Get("state"), Issuer: openAIIssuer, ClientID: clientID}
	if paste {
		input = Relayed{ConnectionID: caps.ConnectionID, URL: q.Get("redirect_uri") + "?" + url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {openAIIssuer}, "client_id": {clientID}}.Encode()}
	}
	return s.Complete(t.Context(), pending.ID, input)
}

func TestSignInPersistsCredentialsAndNeverRebindsOwner(t *testing.T) {
	f, s, w := signInSetup(t)
	first, err := completeSignIn(t, s, w, Login, "subject-one", "", false)
	if err != nil || first.Status != OK || first.Person != "owner" || first.ID == "" {
		t.Fatalf("initial sign-in: %+v %v", first, err)
	}
	credential, err := f.s.Credential(t.Context(), first.ID)
	if err != nil || bearer(t, credential) != "Bearer access:subject-one" {
		t.Fatalf("stored credential: %v", err)
	}
	want := OwnerBinding{Person: "owner", Issuer: openAIIssuer, Subject: "subject-one"}
	if _, err := completeSignIn(t, s, w, Login, "subject-two", "", true); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("second owner login: %v", err)
	}
	second, err := completeSignIn(t, s, w, AddProvider, "subject-two", "", true)
	if err != nil || second.ID == first.ID {
		t.Fatalf("additional provider: %v", err)
	}
	if binding, err := NewOwnerBindings(f.db).Get(t.Context()); err != nil || binding != want {
		t.Fatalf("owner changed: %+v %v", binding, err)
	}
	if _, err := completeSignIn(t, s, w, Login, "subject-two", first.ID, false); !errors.Is(err, ErrSignInIdentity) {
		t.Fatalf("selected registration identity replaced: %v", err)
	}
	again, err := completeSignIn(t, s, w, Login, "subject-one", first.ID, false)
	if err != nil || again.ID != first.ID || again.ClientID != first.ClientID || again.HostID != first.HostID {
		t.Fatalf("reauthorization changed registration: %+v %v", again, err)
	}
	raw, _ := json.Marshal(again)
	if strings.Contains(string(raw), "access:") || strings.Contains(fmt.Sprintf("%+v", again), "refresh:") {
		t.Fatal("account returned credentials")
	}
	var count int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM accounts").Scan(&count)
	}); err != nil || count != 2 {
		t.Fatalf("failed login added account: count=%d error=%v", count, err)
	}
}

func TestSignInReopenKeepsHostRegistrationAndBinding(t *testing.T) {
	f, s, w := signInSetup(t)
	first, err := completeSignIn(t, s, w, Login, "subject-one", "", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	f.s.Close()
	reopened, err := Open(t.Context(), f.db, f.vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	signer, err := NewSignIn(t.Context(), SignInConfig{Store: reopened, Person: "owner", Leaser: signInLeaser{}, Transport: w})
	if err != nil {
		t.Fatal(err)
	}
	defer signer.Close()
	again, err := completeSignIn(t, signer, w, Login, "subject-one", first.ID, true)
	if err != nil || again.HostID != first.HostID || again.ID != first.ID {
		t.Fatalf("reopen: %+v %v", again, err)
	}
}

func TestSignInPersistenceFailureLeavesOwnerUnbound(t *testing.T) {
	f, s, wire := signInSetup(t)
	if err := f.vault.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := completeSignIn(t, s, wire, Login, "subject-one", "", false); !errors.Is(err, ErrStorage) {
		t.Fatalf("credential write failure: %v", err)
	}
	if _, err := NewOwnerBindings(f.db).Get(t.Context()); !errors.Is(err, ErrOwnerUnbound) {
		t.Fatalf("failed sign-in bound an owner: %v", err)
	}
}

func TestClosedSignInCannotStartAnotherAttempt(t *testing.T) {
	_, s, _ := signInSetup(t)
	s.Close()
	if _, err := s.Begin(t.Context(), provider.ChatGPT, RelayCaps{ConnectionID: "connection", Purpose: Login}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed sign-in restarted: %v", err)
	}
}

func TestSignInReplacementWaitsForActiveTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, s, wire := signInSetup(t)
		first, err := completeSignIn(t, s, wire, Login, "subject-one", "", false)
		if err != nil {
			t.Fatal(err)
		}
		release := f.vault.BeginTurn()
		done := make(chan error, 1)
		go func() { _, err := completeSignIn(t, s, wire, Login, "subject-one", first.ID, false); done <- err }()
		synctest.Wait()
		select {
		case err := <-done:
			release()
			t.Fatalf("replacement did not wait: %v", err)
		default:
		}
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
