// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func bindingDB(t *testing.T) *canon.Store {
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
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: OwnerBindingSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func TestOwnerBindingNeverChanges(t *testing.T) {
	db := bindingDB(t)
	store := NewOwnerBindings(db)
	want := OwnerBinding{Person: "owner", Issuer: "https://auth.openai.com", Subject: "subject-one"}
	if err := store.Bind(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if err := store.Bind(t.Context(), want); err != nil {
		t.Fatalf("same identity: %v", err)
	}
	for _, other := range []OwnerBinding{
		{Person: "owner", Issuer: want.Issuer, Subject: "subject-two"},
		{Person: "owner", Issuer: "https://issuer.invalid", Subject: want.Subject},
		{Person: "other-person", Issuer: want.Issuer, Subject: want.Subject},
	} {
		if err := store.Bind(t.Context(), other); !errors.Is(err, ErrOwnerMismatch) {
			t.Fatalf("accepted replacement: %v", err)
		}
	}
	// A newly opened account facade must read the canonical binding.
	got, err := NewOwnerBindings(db).Get(t.Context())
	if err != nil || got != want {
		t.Fatalf("binding changed: %+v %v", got, err)
	}
}

func TestConcurrentFirstSignInsBindOneIdentity(t *testing.T) {
	store := NewOwnerBindings(bindingDB(t))
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, subject := range []string{"one", "two"} {
		workers.Go(func() {
			<-start
			results <- store.Bind(t.Context(), OwnerBinding{Person: "owner", Issuer: "https://auth.openai.com", Subject: subject})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var success, mismatch int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrOwnerMismatch):
			mismatch++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || mismatch != 1 {
		t.Fatalf("success=%d mismatch=%d", success, mismatch)
	}
}

func TestBindingRejectsEmptyIdentityWithoutWriting(t *testing.T) {
	store := NewOwnerBindings(bindingDB(t))
	for _, binding := range []OwnerBinding{{}, {Person: "owner"}, {Person: "owner", Issuer: "https://auth.openai.com"}} {
		if err := store.Bind(t.Context(), binding); !errors.Is(err, ErrSignInInvalid) {
			t.Fatalf("incomplete identity: %v", err)
		}
	}
	if _, err := store.Get(t.Context()); !errors.Is(err, ErrOwnerUnbound) {
		t.Fatalf("binding exists: %v", err)
	}
}

func TestAdditionalProviderCannotReplaceOwnerAndFailedAdmissionRollsBack(t *testing.T) {
	db := bindingDB(t)
	store := NewOwnerBindings(db)
	first := OwnerBinding{Person: "owner", Issuer: "https://auth.openai.com", Subject: "one"}
	failed := errors.New("synthetic account persistence failure")
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if err := admitOwner(t.Context(), tx, first, true); err != nil {
			return err
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context()); !errors.Is(err, ErrOwnerUnbound) {
		t.Fatalf("failed account created a binding: %v", err)
	}
	// The first successful provider sign-in also establishes an unbound owner.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return admitOwner(t.Context(), tx, first, false) }); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Subject = "two"
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return admitOwner(t.Context(), tx, second, false) }); err != nil {
		t.Fatalf("additional provider account: %v", err)
	}
	if got, err := store.Get(t.Context()); err != nil || got != first {
		t.Fatalf("provider replaced binding: %+v %v", got, err)
	}
	second.Person = "different-person"
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { return admitOwner(t.Context(), tx, second, false) }); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("different person admitted: %v", err)
	}
}
