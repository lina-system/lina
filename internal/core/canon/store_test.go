// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func storeConfig(t *testing.T) Config {
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
	return Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 7, Controls: controls,
		Migrations: []Migration{{Version: 1, SQL: "CREATE TABLE items(id INTEGER PRIMARY KEY, body TEXT NOT NULL)"}}}
}

func openStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

func insertItem(tx *Tx) error {
	ctx := tx.ctx
	_, err := tx.ExecContext(ctx, "INSERT INTO items(body) VALUES (?)", "kept")
	return err
}

func itemCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	err := s.Read(t.Context(), func(tx *Tx) error { return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM items").Scan(&count) })
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWriterFenceIsFirstAndLatches(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	var statements []string
	if err := s.writer.Raw(func(raw any) error {
		return raw.(driver.Conn).Raw().Trace(sqlite3.TRACE_STMT, func(_ sqlite3.TraceEvent, _, value any) error {
			statements = append(statements, value.(string))
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	// The driver traces prepared statements; BEGIN/COMMIT use its direct API.
	if len(statements) != 2 || statements[0] != fenceSQL {
		t.Fatalf("transaction statements: %q", statements)
	}
	// A newer generation takes ownership while this writer remains open.
	other, err := OpenSQLite(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	execSQL(t, other, "UPDATE canon_meta SET epoch='8'")
	called := false
	if err := s.Write(t.Context(), func(*Tx) error { called = true; return nil }); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale: %v", err)
	}
	if called {
		t.Fatal("stale writer ran callback")
	}
	execSQL(t, other, "UPDATE canon_meta SET epoch='7'")
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("latch cleared: %v", err)
	}
	if itemCount(t, s) != 1 {
		t.Fatal("stale write persisted")
	}
}

func TestUnrecoverableIOHalts(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	// Existing connections remain readable, but a read-only reopen now fails.
	if err := os.Rename(cfg.Path, cfg.Path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(*Tx) error { return sqlite3.IOERR_READ }); !errors.Is(err, ErrWriteHalted) {
		t.Fatal(err)
	}
	if _, err := cfg.Controls.Read("halt.json"); err != nil {
		t.Fatal(err)
	}
	if itemCount(t, s) != 1 {
		t.Fatal("halt disabled reads")
	}
}

func TestHaltMarkerFailureStaysLatched(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	// A marker publication failure must not reopen the writer's write path.
	// A non-private destination makes durable publication fail without making
	// the already-open store's control directory handle invalid.
	markerPath := filepath.Join(filepath.Dir(cfg.Path), "system/core/halt.json")
	if err := os.WriteFile(markerPath, []byte("unreadable marker"), 0644); err != nil {
		t.Fatal(err)
	}
	err := s.Write(t.Context(), func(*Tx) error { return sqlite3.CORRUPT })
	if !errors.Is(err, ErrWriteHalted) || !errors.Is(err, sqlite3.CORRUPT) {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrWriteHalted) {
		t.Fatal(err)
	}
	if itemCount(t, s) != 1 {
		t.Fatal("lost readable data")
	}
}

func TestCorruptOpenWritesHalt(t *testing.T) {
	cfg := storeConfig(t)
	if err := os.WriteFile(cfg.Path, []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), cfg); !errors.Is(err, ErrWriteHalted) {
		t.Fatal(err)
	}
	if marker, err := cfg.Controls.Read("halt.json"); err != nil || validateHalt(marker) != nil {
		t.Fatalf("marker %s %v", marker, err)
	}
}

func TestMalformedControlAndEpochMetadataAreRefusedUnchanged(t *testing.T) {
	for _, kind := range []string{"halt", "epoch", "schema"} {
		t.Run(kind, func(t *testing.T) {
			cfg := storeConfig(t)
			s := openStore(t, cfg)
			s.Close(context.Background())
			if kind == "halt" {
				if err := cfg.Controls.Write("halt.json", []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := OpenSQLite(t.Context(), cfg.Path)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "epoch" {
					execSQL(t, db, "UPDATE canon_meta SET epoch='01'")
				} else {
					execSQL(t, db, "PRAGMA user_version=-1")
				}
				db.Close()
			}
			before := snapshotFiles(t, filepath.Dir(cfg.Path))
			if s, err := Open(t.Context(), cfg); err == nil {
				s.Close(context.Background())
				t.Fatal("malformed metadata accepted")
			}
			if after := snapshotFiles(t, filepath.Dir(cfg.Path)); !reflect.DeepEqual(before, after) {
				t.Fatal("malformed metadata rewritten")
			}
		})
	}
}

func TestInspectAndNewerSchemaInLiveWALLeaveAllFilesUnchanged(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	other, err := OpenSQLite(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	execSQL(t, other, "PRAGMA wal_autocheckpoint=0")
	execSQL(t, other, "PRAGMA user_version=2")
	before := snapshotFiles(t, filepath.Dir(cfg.Path))
	version, epoch, err := Inspect(t.Context(), cfg.Path)
	if err != nil || version != 2 || epoch != cfg.Epoch {
		t.Fatalf("live WAL inspection: %d %d %v", version, epoch, err)
	}
	if newer, err := Open(t.Context(), cfg); !errors.Is(err, ErrNewerSchema) {
		if newer != nil {
			newer.Close(context.Background())
		}
		t.Fatal(err)
	}
	if after := snapshotFiles(t, filepath.Dir(cfg.Path)); !reflect.DeepEqual(before, after) {
		t.Fatal("inspection modified source files")
	}
}

func TestCloseCancelsActiveWriter(t *testing.T) {
	s := openStore(t, storeConfig(t))
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.Write(t.Context(), func(tx *Tx) error { close(entered); <-tx.ctx.Done(); return tx.ctx.Err() })
	}()
	<-entered
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCallbacksCannotBypassTransactionOwnership(t *testing.T) {
	s := openStore(t, storeConfig(t))
	for _, query := range []string{"COMMIT", "ROLLBACK", "SAVEPOINT bypass", "UPDATE canon_meta SET epoch='0'", "DROP TABLE canon_meta", "PRAGMA user_version=999"} {
		if err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), query); return err }); err == nil {
			t.Fatalf("allowed %s", query)
		}
	}
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
}

func TestReaderRejectsWritesWithClosedCode(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReader(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	if err := reader.Write(t.Context(), insertItem); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("reader write: %v", err)
	}
	if err := reader.Read(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), "DELETE FROM items"); return err }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read transaction write: %v", err)
	}
	if itemCount(t, reader) != 1 {
		t.Fatal("reader changed rows")
	}
}

func TestMigrationsAreNumberedAtomicAndForwardOnly(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(cfg.Migrations, Migration{Version: 2, SQL: "CREATE TABLE halfway(id INTEGER); INSERT INTO missing VALUES (1)"})
	if s, err := Open(t.Context(), cfg); err == nil {
		s.Close(context.Background())
		t.Fatal("bad migration succeeded")
	}
	db, err := OpenSQLiteReader(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := textSQL(t, db, "PRAGMA user_version"); got != "1" {
		t.Fatal(got)
	}
	if got := textSQL(t, db, "SELECT count(*) FROM sqlite_schema WHERE name='halfway'"); got != "0" {
		t.Fatal("partial migration committed")
	}
	db.Close()
	cfg.Migrations[1].SQL = "CREATE TABLE halfway(id INTEGER)"
	s := openStore(t, cfg)
	s.Close(context.Background())
	// Opening an already migrated store must not replay CREATE TABLE.
	s = openStore(t, cfg)
	if version, epoch, err := Inspect(t.Context(), cfg.Path); err != nil || version != 2 || epoch != cfg.Epoch {
		t.Fatalf("inspect=%d,%d,%v", version, epoch, err)
	}
	bad := cfg
	bad.Migrations = []Migration{{Version: 2, SQL: "SELECT 1"}}
	if s, err := Open(t.Context(), bad); err == nil {
		s.Close(context.Background())
		t.Fatal("migration gap accepted")
	}
}

func TestCorruptionHaltsDurablyAndPreservesReads(t *testing.T) {
	for _, code := range []sqlite3.ErrorCode{sqlite3.CORRUPT, sqlite3.NOTADB} {
		t.Run(code.Error(), func(t *testing.T) {
			cfg := storeConfig(t)
			s := openStore(t, cfg)
			if err := s.Write(t.Context(), insertItem); err != nil {
				t.Fatal(err)
			}
			if err := s.writer.Raw(func(raw any) error {
				return raw.(driver.Conn).Raw().CreateFunction("inject_failure", 0, 0, func(ctx sqlite3.Context, _ ...sqlite3.Value) { ctx.ResultError(code) })
			}); err != nil {
				t.Fatal(err)
			}
			err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), "SELECT inject_failure()"); return err })
			if !errors.Is(err, ErrWriteHalted) {
				t.Fatalf("corruption: %v", err)
			}
			marker, err := cfg.Controls.Read("halt.json")
			if err != nil || len(marker) == 0 {
				t.Fatalf("halt marker: %s %v", marker, err)
			}
			if itemCount(t, s) != 1 {
				t.Fatal("reads unavailable after halt")
			}
			if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrWriteHalted) {
				t.Fatal(err)
			}
			s.Close(context.Background())
			cfg.Epoch++
			s = openStore(t, cfg)
			if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrWriteHalted) {
				t.Fatalf("restart cleared halt: %v", err)
			}
			after, _ := cfg.Controls.Read("halt.json")
			if !reflect.DeepEqual(marker, after) {
				t.Fatal("restart changed marker")
			}
			if itemCount(t, s) != 1 {
				t.Fatal("restart lost readable rows")
			}
		})
	}
}

func TestHaltedStoreMirrorsMatchingEpochWithoutWriting(t *testing.T) {
	cfg := storeConfig(t)
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(*Tx) error { return sqlite3.CORRUPT }); !errors.Is(err, ErrWriteHalted) {
		t.Fatalf("halt store: %v", err)
	}
	marker, err := cfg.Controls.Read("halt.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	s = openStore(t, cfg)
	if err := s.MirrorEpoch(t.Context(), cfg.Epoch); err != nil {
		t.Fatalf("matching epoch: %v", err)
	}
	if err := s.MirrorEpoch(t.Context(), cfg.Epoch+1); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("mismatched epoch: %v", err)
	}
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrWriteHalted) {
		t.Fatalf("write while halted: %v", err)
	}
	if got := itemCount(t, s); got != 1 {
		t.Fatalf("read count: %d", got)
	}
	after, err := cfg.Controls.Read("halt.json")
	if err != nil || !reflect.DeepEqual(marker, after) {
		t.Fatalf("halt marker changed: %s %v", after, err)
	}
}

func TestFullAndRecoverableIOErrorsDoNotHalt(t *testing.T) {
	for _, failure := range []error{sqlite3.FULL, sqlite3.IOERR_READ} {
		cfg := storeConfig(t)
		s := openStore(t, cfg)
		if err := s.Write(t.Context(), func(*Tx) error { return failure }); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if _, err := cfg.Controls.Read("halt.json"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected halt: %v", err)
		}
		if err := s.Write(t.Context(), insertItem); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriterSerializesAndRollsBackCancellation(t *testing.T) {
	s := openStore(t, storeConfig(t))
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := s.Write(t.Context(), insertItem); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if itemCount(t, s) != 12 {
		t.Fatal("lost writes")
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := s.Write(ctx, func(tx *Tx) error {
		if err := insertItem(tx); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if itemCount(t, s) != 12 {
		t.Fatal("cancelled write committed")
	}
	if err := s.Write(t.Context(), insertItem); err != nil {
		t.Fatalf("writer could not continue after cancellation: %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestEpochUsesFullUnsignedRange(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Epoch = math.MaxUint64 - 1
	s := openStore(t, cfg)
	if _, epoch, err := Inspect(t.Context(), cfg.Path); err != nil || epoch != cfg.Epoch {
		t.Fatalf("epoch=%d %v", epoch, err)
	}
	other, err := OpenSQLite(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	execSQL(t, other, "UPDATE canon_meta SET epoch=?", strconv.FormatUint(math.MaxUint64, 10))
	if err := s.Write(t.Context(), insertItem); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("unsigned fence: %v", err)
	}
}
