// SPDX-License-Identifier: Apache-2.0
package inbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/inbox"
)

func database(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls,
		Migrations: []canon.Migration{{Version: 1, SQL: canon.InboxSchema}}}
	return open(t, cfg), cfg
}

func open(t *testing.T, cfg canon.Config) *canon.Store {
	t.Helper()
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return db
}

func count(t *testing.T, db *canon.Store) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func input() inbox.Input {
	return inbox.Input{Key: inbox.Key{Conversation: "conversation-a", Source: "client", NativeID: "message-1"}, Body: []byte("original")}
}

func TestDuplicateKeyCommitsOneRowAcrossCallersAndRestart(t *testing.T) {
	db, cfg := database(t)
	s := inbox.New(db)
	first, err := s.Admit(t.Context(), input())
	if err != nil || first.ID <= 0 || !first.Inserted {
		t.Fatalf("first receipt: %+v, %v", first, err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			duplicate := input()
			duplicate.Body = []byte("must not replace original")
			receipt, err := inbox.New(db).Admit(t.Context(), duplicate)
			if err != nil || receipt.ID != first.ID || receipt.Inserted {
				t.Errorf("duplicate receipt: %+v, %v", receipt, err)
			}
		})
	}
	wg.Wait()
	if got := count(t, db); got != 1 {
		t.Fatalf("rows = %d", got)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = open(t, cfg)
	s = inbox.New(db)
	receipt, err := s.Admit(t.Context(), input())
	if err != nil || receipt.ID != first.ID || receipt.Inserted {
		t.Fatalf("restart receipt: %+v, %v", receipt, err)
	}
	stored, err := s.Get(t.Context(), input().Key)
	if err != nil || stored.ID != first.ID || string(stored.Body) != "original" {
		t.Fatalf("stored: %+v, %v", stored, err)
	}
	if got := count(t, db); got != 1 {
		t.Fatalf("rows after restart = %d", got)
	}
}

func TestEveryKeyFieldParticipatesInUniqueness(t *testing.T) {
	db, _ := database(t)
	s := inbox.New(db)
	for _, key := range []inbox.Key{
		{Conversation: "a", Source: "client", NativeID: "1"},
		{Conversation: "b", Source: "client", NativeID: "1"},
		{Conversation: "a", Source: "channel", NativeID: "1"},
		{Conversation: "a", Source: "client", NativeID: "2"},
	} {
		r, err := s.Admit(t.Context(), inbox.Input{Key: key})
		if err != nil || !r.Inserted {
			t.Fatalf("key %+v: %+v, %v", key, r, err)
		}
	}
	if got := count(t, db); got != 4 {
		t.Fatalf("rows = %d", got)
	}
}

// Observe the real transaction before canon commits it, and optionally force
// rollback after the insert succeeds. No fake storage or timing window is used.
type observedCanon struct {
	*canon.Store
	beforeCommit func()
	failure      error
}

func (db observedCanon) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return db.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		db.beforeCommit()
		return db.failure
	})
}

func TestAcknowledgementFollowsCommit(t *testing.T) {
	db, cfg := database(t)
	observed := false
	s := inbox.New(observedCanon{Store: db, beforeCommit: func() {
		observed = true
		var n int
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox").Scan(&n)
		})
		if err != nil || n != 0 {
			t.Errorf("before commit: rows=%d, error=%v", n, err)
		}
	}})
	receipt, err := s.Admit(t.Context(), input())
	if err != nil || receipt.ID <= 0 || !observed {
		t.Fatalf("receipt: %+v, %v, observed=%v", receipt, err, observed)
	}
	reader, err := canon.OpenReader(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	if got := count(t, reader); got != 1 {
		t.Fatalf("acknowledged before commit: %d", got)
	}
}

func TestRollbackAndCancellationNeverAcknowledge(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "cancellation"}[cancelRequest], func(t *testing.T) {
			db, _ := database(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("transaction failed")
			observer := observedCanon{Store: db, failure: failure, beforeCommit: func() {}}
			if cancelRequest {
				observer.failure = nil
				observer.beforeCommit = cancel
				failure = context.Canceled
			}
			receipt, err := inbox.New(observer).Admit(ctx, input())
			if !errors.Is(err, failure) || receipt != (inbox.Receipt{}) {
				t.Fatalf("failed write acknowledged: %+v, %v", receipt, err)
			}
			if got := count(t, db); got != 0 {
				t.Fatalf("rolled back rows = %d", got)
			}
			r, err := inbox.New(db).Admit(t.Context(), input())
			if err != nil || !r.Inserted {
				t.Fatalf("retry: %+v, %v", r, err)
			}
		})
	}
}

func TestCommitFailureNeverAcknowledges(t *testing.T) {
	db, _ := database(t)
	// The insert and trigger succeed; the deferred foreign key fails at COMMIT.
	err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TABLE parent(id INTEGER PRIMARY KEY);
		CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_commit AFTER INSERT ON inbox BEGIN INSERT INTO child VALUES (1); END;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := inbox.New(db).Admit(t.Context(), input())
	if err == nil || receipt != (inbox.Receipt{}) {
		t.Fatalf("commit failure acknowledged: %+v, %v", receipt, err)
	}
	if got := count(t, db); got != 0 {
		t.Fatalf("failed commit rows = %d", got)
	}
}

func TestInvalidKeyAndUnavailableWriterNeverAcknowledge(t *testing.T) {
	db, cfg := database(t)
	s := inbox.New(db)
	for _, key := range []inbox.Key{{}, {Conversation: "a", Source: "s"}, {Conversation: "a", NativeID: "n"}, {Source: "s", NativeID: "n"}} {
		r, err := s.Admit(t.Context(), inbox.Input{Key: key})
		if !errors.Is(err, inbox.ErrInvalid) || r != (inbox.Receipt{}) {
			t.Fatalf("invalid key: %+v, %v", r, err)
		}
	}
	if _, err := s.Get(t.Context(), input().Key); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
	reader, err := canon.OpenReader(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	r, err := inbox.New(reader).Admit(t.Context(), input())
	if !errors.Is(err, canon.ErrReadOnly) || r != (inbox.Receipt{}) {
		t.Fatalf("reader: %+v, %v", r, err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, err = s.Admit(t.Context(), input())
	if !errors.Is(err, canon.ErrClosed) || r != (inbox.Receipt{}) {
		t.Fatalf("closed: %+v, %v", r, err)
	}
}
