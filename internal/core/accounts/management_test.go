// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/thisisjun786/lina/kit/provider"
)

func TestAccountListAndSignOutUseExistingStore(t *testing.T) {
	f := setup(t, nil)
	api, ok := any(f.s).(interface {
		List(context.Context, string) ([]Account, error)
		SignOut(context.Context, string, provider.AccountID) error
	})
	if !ok {
		t.Fatal("existing account store has no list/sign-out boundary")
	}
	rows, err := api.List(t.Context(), "owner")
	if err != nil || len(rows) != 0 {
		t.Fatal("empty account list failed")
	}
}

func TestSignOutDeletesCredentialsAndRetainsRegistration(t *testing.T) {
	f := setup(t, nil)
	add(t, f, "first")
	add(t, f, "second")
	if err := f.s.SignOut(t.Context(), "foreign", "first"); err != ErrNotFound {
		t.Fatal("foreign sign-out accepted")
	}
	rows, err := f.s.List(t.Context(), "person")
	if err != nil || len(rows) != 2 {
		t.Fatal("list omitted accounts")
	}
	if err := f.s.SignOut(t.Context(), "person", "first"); err != nil {
		t.Fatal(err)
	}
	saved, err := f.s.Get(t.Context(), "first")
	if err != nil || saved.Status != NeedsSignIn || saved.ClientID != "client" {
		t.Fatal("sign-out changed registration or remained usable")
	}
	if _, err := f.vault.GetTokens(key(saved)); err == nil {
		t.Fatal("sign-out retained tokens")
	}
	if _, err := f.s.Credential(t.Context(), "first"); err == nil {
		t.Fatal("signed-out account released a credential")
	}
	if _, err := f.s.Credential(t.Context(), "second"); err != nil {
		t.Fatal("sign-out affected another account")
	}
	if err := f.s.SignOut(t.Context(), "person", "first"); err != nil {
		t.Fatal("repeated sign-out failed", err)
	}
}

func TestSignOutWaitsForActiveTurnBeforeRetiringCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := setup(t, nil)
		add(t, f, "pinned")
		end := f.vault.BeginTurn()
		defer end()
		done := make(chan error, 1)
		go func() { done <- f.s.SignOut(t.Context(), "person", "pinned") }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("sign-out returned before the active turn ended: %v", err)
		default:
		}
		end()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, err := f.vault.GetTokens(key(account("pinned"))); err == nil {
			t.Fatal("sign-out retained credentials")
		}
	})
}

func TestSignOutCancellationWhileWaitingPreservesAccount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := setup(t, nil)
		add(t, f, "pinned")
		end := f.vault.BeginTurn()
		defer end()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- f.s.SignOut(ctx, "person", "pinned") }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled sign-out returned %v", err)
		}
		end()
		synctest.Wait()
		saved, err := f.s.Get(t.Context(), "pinned")
		if err != nil || saved.Status != OK {
			t.Fatal("cancelled wait changed account metadata", err)
		}
		if _, err := f.vault.GetTokens(key(saved)); err != nil {
			t.Fatal("cancelled wait changed credentials", err)
		}
	})
}
