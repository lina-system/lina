// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingIsBoundToConnectionAndConsumedBeforeExchange(t *testing.T) {
	now := time.Unix(1800000000, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	flow := newPendingSignIns(func() time.Time { return now }, func(ctx context.Context, a authorization, r Relayed) (authorizedTokens, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return authorizedTokens{identity: verifiedIdentity{Issuer: openAIIssuer, Subject: "one"}}, nil
		case <-ctx.Done():
			return authorizedTokens{}, ctx.Err()
		}
	})
	pending, err := flow.begin(t.Context(), "connection-one", "host-one", signInSelection{login: true}, 32123)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := flow.complete(t.Context(), "connection-two", pending.ID, Relayed{}); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("other connection accepted: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("foreign connection exchanged the code")
	}
	done := make(chan error, 1)
	go func() {
		_, selected, err := flow.complete(t.Context(), "connection-one", pending.ID, Relayed{})
		if err == nil && !selected.login {
			err = errors.New("lost login purpose")
		}
		done <- err
	}()
	<-entered
	if _, _, err := flow.complete(t.Context(), "connection-one", pending.ID, Relayed{}); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("replay accepted: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("exchange ran %d times", calls.Load())
	}
}

func TestDisconnectAndReplacementCancelPendingSignIn(t *testing.T) {
	now := time.Unix(1800000000, 0)
	entered := make(chan struct{})
	flow := newPendingSignIns(func() time.Time { return now }, func(ctx context.Context, _ authorization, _ Relayed) (authorizedTokens, error) {
		close(entered)
		<-ctx.Done()
		return authorizedTokens{}, ctx.Err()
	})
	first, err := flow.begin(t.Context(), "connection-one", "host-one", signInSelection{}, 32123)
	if err != nil {
		t.Fatal(err)
	}
	second, err := flow.begin(t.Context(), "connection-one", "host-one", signInSelection{}, 32123)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := flow.complete(t.Context(), "connection-one", first.ID, Relayed{}); !errors.Is(err, ErrSignInInvalid) {
		t.Fatalf("replaced attempt: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := flow.complete(t.Context(), "connection-one", second.ID, Relayed{}); done <- err }()
	<-entered
	flow.disconnect("connection-one")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("disconnect: %v", err)
	}
}

func TestExpiredPendingIsNotExchanged(t *testing.T) {
	now := time.Unix(1800000000, 0)
	calls := 0
	flow := newPendingSignIns(func() time.Time { return now }, func(context.Context, authorization, Relayed) (authorizedTokens, error) {
		calls++
		return authorizedTokens{}, nil
	})
	pending, err := flow.begin(t.Context(), "connection-one", "host-one", signInSelection{}, 32123)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(signInLifetime)
	if _, _, err := flow.complete(t.Context(), "connection-one", pending.ID, Relayed{}); !errors.Is(err, ErrSignInExpired) {
		t.Fatalf("expired: %v", err)
	}
	if calls != 0 {
		t.Fatal("expired attempt exchanged")
	}
}
