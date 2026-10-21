// SPDX-License-Identifier: Apache-2.0
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

type fixture struct {
	s     *Store
	db    *canon.Store
	vault *secrets.Store
	root  string
}

func setup(t *testing.T, refresh Refresher, backends ...secrets.Backend) fixture {
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
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	var vault *secrets.Store
	if len(backends) > 0 {
		vault, err = secrets.NewStore(backends[0])
	} else {
		vault, err = secrets.OpenFiles(root)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	s, err := Open(t.Context(), db, vault, refresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return fixture{s, db, vault, root}
}
func account(id string) Account {
	return Account{ID: provider.AccountID(id), Person: "person", Provider: provider.ChatGPT, Label: "Synthetic", Auth: provider.LoopbackOAuth, ClientID: "client", HostID: "host", Scopes: []string{"chatgpt.tokens.use.direct"}}
}
func rotated(access, refresh string) Rotated {
	at := time.Now().Add(time.Hour)
	rt := time.Now().Add(30 * 24 * time.Hour)
	return Rotated{Tokens: secrets.NewTokens(access, refresh), AccessExpires: &at, RefreshExpires: &rt}
}
func add(t *testing.T, f fixture, id string) {
	t.Helper()
	if err := f.s.Add(t.Context(), account(id), rotated("access-original", "refresh-original")); err != nil {
		t.Fatal(err)
	}
}
func bearer(t *testing.T, c provider.Credential) string {
	t.Helper()
	r, _ := http.NewRequest("POST", "https://example.invalid", nil)
	if err := c.Authorize(r); err != nil {
		t.Fatal(err)
	}
	return r.Header.Get("Authorization")
}
func due(t *testing.T, f fixture, id string) {
	t.Helper()
	// Move only the persisted expiry to make admission and the actor observe the same due record.
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		a, err := get(t.Context(), tx, provider.AccountID(id))
		if err != nil {
			return err
		}
		at := time.Now().Add(-time.Second)
		a.AccessExpires = &at
		return put(t.Context(), tx, a)
	}); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentRefreshPersistsRotationBeforeUse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := setup(t, func(ctx context.Context, a Account, token secret.Secret) (Rotated, error) {
		if a.ID != "one" {
			return Rotated{}, errors.New("wrong account")
		}
		h := http.Header{}
		if err := secret.InjectHeader(h, "Refresh", "", token); err != nil || h.Get("Refresh") != "refresh-original" {
			return Rotated{}, errors.New("wrong refresh")
		}
		calls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return Rotated{}, ctx.Err()
		}
		return rotated("access-rotated", "refresh-rotated"), nil
	})
	add(t, f, "one")
	due(t, f, "one")
	const n = 24
	var wg sync.WaitGroup
	results := make(chan provider.Credential, n)
	errs := make(chan error, n)
	for range n {
		wg.Go(func() { c, err := f.s.Credential(t.Context(), "one"); results <- c; errs <- err })
	}
	<-entered
	a, err := f.s.Get(t.Context(), "one")
	if err != nil || a.Status != NeedsSignIn {
		t.Fatalf("refresh intent not durable: %+v %v", a, err)
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for c := range results {
		if bearer(t, c) != "Bearer access-rotated" {
			t.Fatal("old access token")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls %d", calls.Load())
	}
	f.s.Close()
	f.vault.Close()
	vault, err := secrets.OpenFiles(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	s, err := Open(t.Context(), f.db, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Credential(t.Context(), "one")
	if err != nil || bearer(t, c) != "Bearer access-rotated" {
		t.Fatalf("rotation not durable: %v", err)
	}
	tokens, err := vault.GetTokens(key(account("one")))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	secret.InjectHeader(h, "Refresh", "", tokens.Refresh())
	if h.Get("Refresh") != "refresh-rotated" {
		t.Fatal("refresh rotation not durable")
	}
}
func TestRefreshFailureStopsAndNeverUsesAnotherAccount(t *testing.T) {
	var calls atomic.Int32
	f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
		calls.Add(1)
		return Rotated{}, errors.New("provider leaked refresh-original")
	})
	add(t, f, "one")
	add(t, f, "two")
	due(t, f, "one")
	for range 3 {
		_, err := f.s.Credential(t.Context(), "one")
		if err == nil || err.Error() != string(NeedsSignIn) {
			t.Fatalf("unsafe failure %v", err)
		}
	}
	a, err := f.s.Get(t.Context(), "one")
	if err != nil || a.Status != NeedsSignIn {
		t.Fatalf("status %+v %v", a, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("retried %d", calls.Load())
	}
	c, err := f.s.Credential(t.Context(), "two")
	if err != nil || c.Account != "two" {
		t.Fatalf("other account affected: %v", err)
	}
	f.s.Close()
	s, err := Open(t.Context(), f.db, f.vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Credential(t.Context(), "one"); err == nil || err.Error() != string(NeedsSignIn) {
		t.Fatalf("failure lost on reopen: %v", err)
	}
}
func TestStatusesAndSecretBoundaries(t *testing.T) {
	f := setup(t, nil)
	add(t, f, "one")
	for _, status := range []Status{NotEligible, UsageCapped, OK, NeedsSignIn} {
		if err := f.s.SetStatus(t.Context(), "one", status); err != nil {
			t.Fatal(err)
		}
		a, err := f.s.Get(t.Context(), "one")
		if err != nil || a.Status != status {
			t.Fatal(a, err)
		}
		c, err := f.s.Credential(t.Context(), "one")
		if status == OK {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || err.Error() != string(status) {
			t.Fatal(status, err)
		}
		r := rotated("access-original", "refresh-original")
		for _, v := range []any{a, c, r, r.Tokens} {
			encoded, _ := json.Marshal(v)
			for _, out := range []string{string(encoded), fmt.Sprintf("%v %+v %#v", v, v, v)} {
				if strings.Contains(out, "access-original") || strings.Contains(out, "refresh-original") {
					t.Fatal("secret escaped encoding or formatting")
				}
			}
		}
	}
	for _, name := range []string{"canon.sqlite", "canon.sqlite-wal"} {
		b, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "access-original") || strings.Contains(string(b), "refresh-original") {
			t.Fatal("token entered canon")
		}
	}
}

func TestAccountsRefreshIndependentlyAndCloseJoins(t *testing.T) {
	entered := make(chan provider.AccountID, 2)
	f := setup(t, func(ctx context.Context, a Account, _ secret.Secret) (Rotated, error) {
		entered <- a.ID
		<-ctx.Done()
		return Rotated{}, ctx.Err()
	})
	add(t, f, "one")
	add(t, f, "two")
	due(t, f, "one")
	due(t, f, "two")
	var wg sync.WaitGroup
	for _, id := range []provider.AccountID{"one", "two"} {
		wg.Go(func() { _, _ = f.s.Credential(t.Context(), id) })
	}
	first, second := <-entered, <-entered
	if first == second {
		t.Fatal("account actor duplicated")
	}
	f.s.Close()
	wg.Wait()
	for _, id := range []provider.AccountID{"one", "two"} {
		a, err := f.s.Get(t.Context(), id)
		if err != nil || a.Status != NeedsSignIn {
			t.Fatalf("interrupted refresh not fenced: %+v %v", a, err)
		}
	}
	if _, err := f.s.Credential(t.Context(), "one"); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestCanceledWaiterDoesNotCancelSharedRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := setup(t, func(ctx context.Context, _ Account, _ secret.Secret) (Rotated, error) {
		close(entered)
		select {
		case <-release:
			return rotated("new-access", "new-refresh"), nil
		case <-ctx.Done():
			return Rotated{}, ctx.Err()
		}
	})
	add(t, f, "one")
	due(t, f, "one")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := f.s.Credential(ctx, "one"); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(release)
	c, err := f.s.Credential(t.Context(), "one")
	if err != nil || bearer(t, c) != "Bearer new-access" {
		t.Fatalf("shared refresh canceled: %v", err)
	}
}
func TestMissingSecretsAndInterruptedRefreshRequireSignIn(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			var calls atomic.Int32
			f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
				calls.Add(1)
				return rotated("new-access", "new-refresh"), nil
			})
			add(t, f, "one")
			if interrupted {
				if err := f.s.SetStatus(t.Context(), "one", NeedsSignIn); err != nil {
					t.Fatal(err)
				}
			} else if err := f.vault.Delete(key(account("one"))); err != nil {
				t.Fatal(err)
			}
			f.s.Close()
			s, err := Open(t.Context(), f.db, f.vault, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.Credential(t.Context(), "one"); err == nil || err.Error() != string(NeedsSignIn) {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("retried interrupted refresh")
			}
			if err := s.SetStatus(t.Context(), "one", OK); err == nil {
				t.Fatal("reused failed credentials without sign-in")
			}
			if err := s.Replace(t.Context(), "one", rotated("signed-in-access", "signed-in-refresh")); err != nil {
				t.Fatal(err)
			}
			c, err := s.Credential(t.Context(), "one")
			if err != nil || bearer(t, c) != "Bearer signed-in-access" {
				t.Fatal(err)
			}
		})
	}
}
func TestScopeAndAccountIdentityAreEnforced(t *testing.T) {
	f := setup(t, nil)
	a := account("one")
	a.Scopes = []string{"openid"}
	if err := f.s.Add(t.Context(), a, rotated("access", "refresh")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Credential(t.Context(), "one"); err == nil || err.Error() != string(NotEligible) {
		t.Fatal(err)
	}
	if err := f.s.SetStatus(t.Context(), "one", OK); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Credential(t.Context(), "one"); err == nil || err.Error() != string(NotEligible) {
		t.Fatal("status bypassed scope check")
	}
	a.Person = "other"
	if err := f.s.Add(t.Context(), a, rotated("other", "other")); !errors.Is(err, ErrConflict) {
		t.Fatal("account rebound", err)
	}
	if _, err := Open(t.Context(), f.db, f.vault, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("second store admitted", err)
	}
	if _, err := f.s.Credential(t.Context(), "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestAPIKeyAndObservedLoginDoNotRefresh(t *testing.T) {
	f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
		t.Error("unexpected refresh")
		return Rotated{}, ErrInvalid
	})
	a := account("key")
	a.Provider = provider.Gemini
	a.Auth = provider.APIKey
	if err := f.s.Add(t.Context(), a, Rotated{Tokens: secrets.NewTokens("api-key", "")}); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Credential(t.Context(), a.ID)
	if err != nil || bearer(t, c) != "Bearer api-key" {
		t.Fatal(err)
	}
	a.ID = "observed"
	a.Provider = provider.Grok
	a.Auth = provider.ObservedCLILogin
	if err := f.s.Add(t.Context(), a, Rotated{}); err != nil {
		t.Fatal(err)
	}
	c, err = f.s.Credential(t.Context(), a.ID)
	if err != nil || c.Account != a.ID || c.Kind != provider.ObservedCLILogin {
		t.Fatal(c, err)
	}
}

func TestIdleRefreshAndKeepAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
			calls.Add(1)
			return rotated("idle-access", "idle-refresh"), nil
		})
		add(t, f, "one")
		time.Sleep(49 * time.Minute)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("refreshed too early")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("idle refresh calls %d", calls.Load())
		}
		c, err := f.s.Credential(t.Context(), "one")
		if err != nil || bearer(t, c) != "Bearer idle-access" {
			t.Fatal(err)
		}
	})
}

type faultVault struct {
	failRead         atomic.Bool
	reads            atomic.Int32
	values           map[secrets.Key][]byte
	fail             atomic.Bool
	writing, release chan struct{}
	block            atomic.Bool
}

func (v *faultVault) Read(k secrets.Key) ([]byte, error) {
	v.reads.Add(1)
	if v.failRead.Load() {
		return nil, errors.New("keychain unavailable")
	}
	b, ok := v.values[k]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), b...), nil
}
func (v *faultVault) Write(k secrets.Key, b []byte) error {
	if v.block.Load() {
		close(v.writing)
		<-v.release
	}
	if v.fail.Load() {
		return errors.New("storage diagnostic with access-rotated refresh-rotated")
	}
	v.values[k] = append([]byte(nil), b...)
	return nil
}
func (v *faultVault) Delete(k secrets.Key) error { delete(v.values, k); return nil }
func (v *faultVault) Close() error               { return nil }
func TestRotationWaitsForPersistenceAndFailsClosed(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			backend := &faultVault{values: make(map[secrets.Key][]byte), writing: make(chan struct{}), release: make(chan struct{})}
			f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
				return rotated("access-rotated", "refresh-rotated"), nil
			}, backend)
			add(t, f, "one")
			due(t, f, "one")
			backend.block.Store(true)
			backend.fail.Store(fail)
			done := make(chan result, 1)
			go func() { c, err := f.s.Credential(t.Context(), "one"); done <- result{c, err} }()
			<-backend.writing
			select {
			case <-done:
				t.Fatal("credential released before secret persisted")
			default:
			}
			close(backend.release)
			out := <-done
			if fail {
				if out.err == nil || out.err.Error() != string(NeedsSignIn) {
					t.Fatal(out.err)
				}
				if out.credential.Account != "" {
					t.Fatal("released failed rotation")
				}
				a, err := f.s.Get(t.Context(), "one")
				if err != nil || a.Status != NeedsSignIn {
					t.Fatal(a, err)
				}
			} else if out.err != nil || bearer(t, out.credential) != "Bearer access-rotated" {
				t.Fatal(out.err)
			}
		})
	}
}
func TestCanonFailureAfterTokenWriteNeverReleasesCredential(t *testing.T) {
	var db *canon.Store
	f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
		_ = db.MirrorEpoch(context.Background(), 2)
		return rotated("access-rotated", "refresh-rotated"), nil
	})
	db = f.db
	add(t, f, "one")
	due(t, f, "one")
	if c, err := f.s.Credential(t.Context(), "one"); !errors.Is(err, ErrStorage) || c.Account != "" {
		t.Fatal(c, err)
	}
	tokens, err := f.vault.GetTokens(key(account("one")))
	if err != nil || bearer(t, tokens.Credential("one", provider.LoopbackOAuth)) != "Bearer access-rotated" {
		t.Fatal("rotated token lost", err)
	}
	a, err := f.s.Get(t.Context(), "one")
	if err != nil || a.Status != NeedsSignIn {
		t.Fatal(a, err)
	}
}
func TestRefreshWaitsForTurnsAndBlocksNewTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
			calls.Add(1)
			close(entered)
			<-release
			return rotated("access-rotated", "refresh-rotated"), nil
		})
		add(t, f, "one")
		end := f.vault.BeginTurn()
		due(t, f, "one")
		done := make(chan error, 1)
		go func() { _, err := f.s.Credential(t.Context(), "one"); done <- err }()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("refreshed during a turn")
		}
		a, err := f.s.Get(t.Context(), "one")
		if err != nil || a.Status != OK {
			t.Fatal("waiting for turn invalidated account", a, err)
		}
		end()
		<-entered
		began := make(chan struct{})
		go func() { end := f.vault.BeginTurn(); close(began); end() }()
		synctest.Wait()
		select {
		case <-began:
			t.Fatal("turn entered during rotation")
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		<-began
	})
}
func TestIdleKeepAlivePreservesCapAndFailureStopsTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
			if calls.Add(1) == 2 {
				return Rotated{}, errors.New("invalid grant")
			}
			r := rotated("idle-access", "idle-refresh")
			at := time.Now().Add(72 * time.Hour)
			r.AccessExpires = &at
			r.RefreshExpires = nil
			return r, nil
		})
		a := account("one")
		r := rotated("access", "refresh")
		at := time.Now().Add(72 * time.Hour)
		r.AccessExpires = &at
		r.RefreshExpires = nil
		if err := f.s.Add(t.Context(), a, r); err != nil {
			t.Fatal(err)
		}
		if err := f.s.SetStatus(t.Context(), a.ID, UsageCapped); err != nil {
			t.Fatal(err)
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		got, err := f.s.Get(t.Context(), a.ID)
		if err != nil || got.Status != UsageCapped || calls.Load() != 1 {
			t.Fatal(got, err, calls.Load())
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		got, err = f.s.Get(t.Context(), a.ID)
		if err != nil || got.Status != NeedsSignIn || calls.Load() != 2 {
			t.Fatal(got, err, calls.Load())
		}
		time.Sleep(7 * 24 * time.Hour)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal("failed refresh retried")
		}
	})
}

func TestCredentialRemainsOpaqueInsidePrivateWrappers(t *testing.T) {
	f := setup(t, nil)
	add(t, f, "one")
	credential, err := f.s.Credential(t.Context(), "one")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := struct{ credential provider.Credential }{credential}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, wrapper), "access-original") {
			t.Fatal("private wrapper formatting exposed account bearer")
		}
	}
}

func TestInvalidRotationsRequireFreshSignIn(t *testing.T) {
	for _, kind := range []string{"no-refresh", "expired-access", "expired-refresh"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
				calls.Add(1)
				r := rotated("replacement-access", "replacement-refresh")
				past := time.Now().Add(-time.Minute)
				switch kind {
				case "no-refresh":
					r.Tokens = secrets.NewTokens("replacement-access", "")
				case "expired-access":
					r.AccessExpires = &past
				case "expired-refresh":
					r.RefreshExpires = &past
				}
				return r, nil
			})
			add(t, f, "one")
			due(t, f, "one")
			for range 2 {
				c, err := f.s.Credential(t.Context(), "one")
				if err == nil || err.Error() != string(NeedsSignIn) || c.Account != "" {
					t.Fatal("invalid rotation admitted", err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("invalid rotation retried")
			}
		})
	}
}
func TestRefreshExpiryTriggersBeforeLongerAccessExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
			calls.Add(1)
			return rotated("new-access", "new-refresh"), nil
		})
		r := rotated("access", "refresh")
		soon := time.Now().Add(12 * time.Minute)
		r.RefreshExpires = &soon
		if err := f.s.Add(t.Context(), account("one"), r); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("refreshed before half of short lifetime")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("shorter refresh lifetime ignored")
		}
	})
}

func TestCredentialAuthorizationHeader(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		valid       bool
	}{
		{"bearer", "header-fixture", true},
		{"empty", "", false},
		{"carriage-return", "invalid\rheader", false},
		{"line-feed", "invalid\nheader", false},
		{"nul", "invalid\x00header", false},
		{"control", "invalid\x01header", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential := provider.NewCredential("account", provider.LoopbackOAuth, tc.token)
			req, err := http.NewRequest(http.MethodPost, "https://example.invalid/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			err = credential.Authorize(req)
			if tc.valid {
				if err != nil || req.Header.Get("Authorization") != "Bearer header-fixture" {
					t.Fatal("incorrect authorization header", err)
				}
				if credential.String() != "[credential]" || credential.GoString() != "[credential]" {
					t.Fatal("public representation changed")
				}
			} else {
				var failure *provider.Failure
				if !errors.As(err, &failure) || failure.Kind != provider.NeedsSignIn {
					t.Fatal("invalid bearer was accepted")
				}
				if req.Header.Get("Authorization") != "" {
					t.Fatal("invalid bearer reached a header")
				}
			}
		})
	}
}

func TestReplacementWaitsForTurnWithoutDisablingAccount(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := setup(t, nil)
				add(t, f, "one")
				end := f.vault.BeginTurn()
				defer end()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- f.s.Replace(ctx, "one", rotated("signed-in-access", "signed-in-refresh")) }()
				synctest.Wait()
				a, err := f.s.Get(t.Context(), "one")
				if err != nil || a.Status != OK {
					t.Fatal("waiting replacement disabled account", a, err)
				}
				if canceled {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					end()
				} else {
					end()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
				c, err := f.s.Credential(t.Context(), "one")
				if err != nil {
					t.Fatal(err)
				}
				want := "Bearer signed-in-access"
				if canceled {
					want = "Bearer access-original"
				}
				if bearer(t, c) != want {
					t.Fatal("incorrect replacement credential")
				}
			})
		})
	}
}
func TestVerifiedReplacementUpdatesGrantedScopes(t *testing.T) {
	f := setup(t, nil)
	a := account("one")
	a.Scopes = []string{"openid"}
	if err := f.s.Add(t.Context(), a, rotated("access", "refresh")); err != nil {
		t.Fatal(err)
	}
	r := rotated("signed-in-access", "signed-in-refresh")
	r.Scopes = []string{"openid", "chatgpt.tokens.use.direct"}
	if err := f.s.Replace(t.Context(), a.ID, r); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.Credential(t.Context(), a.ID)
	if err != nil || bearer(t, c) != "Bearer signed-in-access" {
		t.Fatal("renewed grant stayed ineligible", err)
	}
	got, err := f.s.Get(t.Context(), a.ID)
	if err != nil || len(got.Scopes) != 2 || got.Status != OK {
		t.Fatal(got, err)
	}
	r.Scopes = []string{}
	if err := f.s.Replace(t.Context(), a.ID, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Credential(t.Context(), a.ID); err == nil || err.Error() != string(NotEligible) {
		t.Fatal("removed grant remained eligible", err)
	}
}
func TestKeychainReadRecoveryDoesNotRequireSignIn(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprint(refresh), func(t *testing.T) {
			backend := &faultVault{values: make(map[secrets.Key][]byte)}
			var calls atomic.Int32
			f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
				calls.Add(1)
				return rotated("access-rotated", "refresh-rotated"), nil
			}, backend)
			add(t, f, "one")
			if refresh {
				due(t, f, "one")
			}
			backend.failRead.Store(true)
			c, err := f.s.Credential(t.Context(), "one")
			if err == nil || c.Account != "" {
				t.Fatal("keychain failure admitted credentials")
			}
			a, err := f.s.Get(t.Context(), "one")
			if err != nil || a.Status != OK {
				t.Fatal("keychain outage invalidated account", a, err)
			}
			if calls.Load() != 0 {
				t.Fatal("refresh contacted issuer without tokens")
			}
			backend.failRead.Store(false)
			c, err = f.s.Credential(t.Context(), "one")
			if err != nil {
				t.Fatal("keychain recovery requires sign-in", err)
			}
			want := "Bearer access-original"
			if refresh {
				want = "Bearer access-rotated"
			}
			if bearer(t, c) != want {
				t.Fatal("incorrect recovered credential")
			}
		})
	}
}

func TestKeychainOutageBacksOffAndRecoversIdleRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &faultVault{values: make(map[secrets.Key][]byte)}
		var calls atomic.Int32
		f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
			calls.Add(1)
			return rotated("access-rotated", "refresh-rotated"), nil
		}, backend)
		add(t, f, "one")
		backend.failRead.Store(true)
		reads := backend.reads.Load()
		time.Sleep(50 * time.Minute)
		synctest.Wait()
		if calls.Load() != 0 || backend.reads.Load() != reads+1 {
			t.Fatal("unexpected idle exchange or reads")
		}
		time.Sleep(59 * time.Second)
		synctest.Wait()
		if backend.reads.Load() != reads+1 {
			t.Fatal("keychain outage caused retry loop")
		}
		backend.failRead.Store(false)
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("idle refresh did not resume after keychain recovery")
		}
		c, err := f.s.Credential(t.Context(), "one")
		if err != nil || bearer(t, c) != "Bearer access-rotated" || calls.Load() != 1 {
			t.Fatal("idle outage did not recover", err)
		}
	})
}
func TestCloseCancelsReplacementWaitingForTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := setup(t, nil)
		add(t, f, "one")
		end := f.vault.BeginTurn()
		defer end()
		done := make(chan error, 1)
		go func() { done <- f.s.Replace(context.Background(), "one", rotated("new-access", "new-refresh")) }()
		synctest.Wait()
		f.s.Close()
		if err := <-done; err != ErrClosed && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		a, err := f.s.Get(t.Context(), "one")
		if err != nil || a.Status != OK {
			t.Fatal(a, err)
		}
	})
}
func TestRefreshCannotReleaseCredentialWithRemovedScope(t *testing.T) {
	f := setup(t, func(context.Context, Account, secret.Secret) (Rotated, error) {
		r := rotated("new-access", "new-refresh")
		r.Scopes = []string{"openid"}
		return r, nil
	})
	add(t, f, "one")
	due(t, f, "one")
	c, err := f.s.Credential(t.Context(), "one")
	if err == nil || err.Error() != string(NotEligible) || c.Account != "" {
		t.Fatal("removed scope admitted a call", err)
	}
}
