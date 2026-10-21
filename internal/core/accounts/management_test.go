// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"testing"

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
