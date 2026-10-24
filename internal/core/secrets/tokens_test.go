// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/secrets"
)

func tokenStore(t *testing.T) *secrets.Store {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := secrets.OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestTokensStayOpaqueInFormattingAndLogs(t *testing.T) {
	tokens := secrets.NewTokens("access-fixture", "refresh-fixture")
	var log bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&log, nil))
	logger.Info("account", "tokens", tokens)
	encoded, err := json.Marshal(struct{ Tokens secrets.Tokens }{tokens})
	if err != nil {
		t.Fatal(err)
	}
	private := struct{ tokens secrets.Tokens }{tokens}
	for _, out := range []string{log.String(), string(encoded), fmt.Sprintf("%v %+v %#v", private, private, private)} {
		if strings.Contains(out, "access-fixture") || strings.Contains(out, "refresh-fixture") {
			t.Fatal("token escaped safe representation")
		}
	}
}
func TestTokensRejectMalformedRecordsAndSanitizeCallbackErrors(t *testing.T) {
	s := tokenStore(t)
	key := secrets.Key{Person: "person", Service: "provider", Name: "account"}
	for _, raw := range []string{`not-json access-fixture`, `{"access":"","refresh":"refresh-fixture"}`, `{"access":"bad\r\nheader","refresh":"refresh-fixture"}`} {
		if err := s.Put(key, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetTokens(key); !errors.Is(err, secrets.ErrInvalid) {
			t.Fatal("malformed record accepted", err)
		}
	}
	original := secrets.NewTokens("access-fixture", "refresh-fixture")
	if err := s.PutTokens(key, original); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTokens(t.Context(), key, func(secrets.Tokens) (secrets.Tokens, error) {
		return secrets.Tokens{}, errors.New("unsafe refresh-fixture")
	}); !errors.Is(err, secrets.ErrUpdate) {
		t.Fatal(err)
	}
	other := key
	other.Person = "other"
	if _, err := s.GetTokens(other); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("credential crossed person boundary", err)
	}
}
func TestRotationReservationBlocksMutationsAndReleasesOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := tokenStore(t)
		key := secrets.Key{Person: "person", Service: "provider", Name: "account"}
		initial := secrets.NewTokens("access-fixture", "refresh-fixture")
		if err := s.PutTokens(key, initial); err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- s.UpdateTokens(t.Context(), key, func(secrets.Tokens) (secrets.Tokens, error) { close(entered); <-release; return initial, nil })
		}()
		<-entered
		if err := s.Delete(key); !errors.Is(err, secrets.ErrBusy) {
			t.Fatal("deleted rotating token", err)
		}
		if err := s.PutTokens(key, initial); !errors.Is(err, secrets.ErrBusy) {
			t.Fatal("overwrote rotating token", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		waiting := make(chan error, 1)
		go func() {
			waiting <- s.UpdateTokens(ctx, key, func(secrets.Tokens) (secrets.Tokens, error) { t.Error("second rotation entered"); return initial, nil })
		}()
		synctest.Wait()
		cancel()
		if err := <-waiting; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		end := s.BeginTurn()
		end()
	})
}

func TestReplacementKeepsTurnExclusionThroughMetadataCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := tokenStore(t)
		key := secrets.Key{Person: "person", Service: "provider", Name: "account"}
		committing, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- s.ReplaceTokens(t.Context(), key, secrets.NewTokens("access-fixture", "refresh-fixture"), func() error { return nil }, func() error { close(committing); <-release; return nil })
		}()
		<-committing
		began := make(chan struct{})
		go func() { end := s.BeginTurn(); close(began); end() }()
		synctest.Wait()
		select {
		case <-began:
			t.Fatal("turn entered before metadata commit")
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		<-began
	})
}

func TestDeletionKeepsReservationThroughIntentAndIgnoresLateCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := tokenStore(t)
		key := secrets.Key{Person: "person", Service: "provider", Name: "account"}
		if err := s.PutTokens(key, secrets.NewTokens("access-fixture", "refresh-fixture")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		committing, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- s.DeleteTokens(ctx, key, func() error { close(committing); <-release; return nil })
		}()
		<-committing
		began := make(chan struct{})
		go func() { end := s.BeginTurn(); close(began); end() }()
		synctest.Wait()
		select {
		case <-began:
			t.Fatal("turn entered before retirement committed")
		default:
		}
		cancel()
		close(release)
		if err := <-done; err != nil {
			t.Fatal("cancellation after admission interrupted retirement", err)
		}
		<-began
		if _, err := s.GetTokens(key); !errors.Is(err, secrets.ErrNotFound) {
			t.Fatal("retirement left the credential readable", err)
		}
	})
}

func TestDeletionRequiresCommittedIntentAndIsIdempotent(t *testing.T) {
	s := tokenStore(t)
	key := secrets.Key{Person: "person", Service: "provider", Name: "account"}
	if err := s.PutTokens(key, secrets.NewTokens("access-fixture", "refresh-fixture")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTokens(t.Context(), key, func() error { return errors.New("unsafe access-fixture") }); !errors.Is(err, secrets.ErrUpdate) {
		t.Fatal("retirement exposed callback error or ignored failed intent", err)
	}
	if _, err := s.GetTokens(key); err != nil {
		t.Fatal("failed intent deleted the credential", err)
	}
	for range 2 {
		if err := s.DeleteTokens(t.Context(), key, func() error { return nil }); err != nil {
			t.Fatal("repeat deletion failed", err)
		}
	}
	end := s.BeginTurn()
	end()
}

func TestTokenExpirySurvivesStorageAndLegacyRecords(t *testing.T) {
	s := tokenStore(t)
	key := secrets.Key{Person: "person", Service: "webex", Name: "tokens"}
	expires := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if err := s.PutTokens(key, secrets.NewTokensUntil("access", "refresh", expires)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTokens(key)
	if err != nil || !got.Expires().Equal(expires) {
		t.Fatalf("expiry lost: %v %v", got.Expires(), err)
	}
	if err := s.Put(key, []byte(`{"access":"old","refresh":"old-refresh"}`)); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTokens(key)
	if err != nil || !got.Expires().IsZero() {
		t.Fatalf("legacy record: %v", err)
	}
}
