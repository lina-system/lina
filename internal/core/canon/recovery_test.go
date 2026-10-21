// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"errors"
	"testing"
)

func TestFailedReconciliationBlocksReadsUntilReplay(t *testing.T) {
	s := openStore(t, storeConfig(t))
	failure := errors.New("durable removal awaits SQLite replay")
	if err := s.Reconcile(func(RecoveryWrite) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	called := false
	err := s.Read(t.Context(), func(*Tx) error { called = true; return nil })
	if !errors.Is(err, ErrRecoveryRequired) || called {
		t.Fatalf("exposed unreconciled state: %v %v", called, err)
	}
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("accepted new work before replay: %v", err)
	}
	if err := s.Reconcile(func(write RecoveryWrite) error { return write(t.Context(), insertItem) }); err != nil {
		t.Fatal(err)
	}
	if itemCount(t, s) != 1 {
		t.Fatal("replay did not reopen reads")
	}
}

func TestReconciliationWaitsForPriorReaderAndPublishesAtomically(t *testing.T) {
	s := openStore(t, storeConfig(t))
	entered, release, complete := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { complete <- s.Read(t.Context(), func(*Tx) error { close(entered); <-release; return nil }) }()
	<-entered
	replayStarted, replayDone := make(chan struct{}), make(chan error, 1)
	go func() {
		replayDone <- s.Reconcile(func(write RecoveryWrite) error { close(replayStarted); return write(t.Context(), insertItem) })
	}()
	// The prior reader owns the read lock; it must finish before reconciliation.
	close(release)
	if err := <-complete; err != nil {
		t.Fatal(err)
	}
	<-replayStarted
	if err := <-replayDone; err != nil {
		t.Fatal(err)
	}
	if itemCount(t, s) != 1 {
		t.Fatal("reconciled value missing")
	}
}
