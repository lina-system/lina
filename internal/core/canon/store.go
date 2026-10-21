// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// Code is the closed set of storage policy failures. SQLite and context errors
// retain their typed causes when no storage policy code applies.
type Code uint8

const (
	ErrStaleEpoch Code = iota + 1
	ErrReadOnly
	ErrWriteHalted
	ErrNewerSchema
	ErrClosed
	ErrInvalid
	ErrRecoveryRequired
)

func (c Code) Error() string {
	names := [...]string{"invalid", "stale_epoch", "read_only", "store_write_halted", "schema_too_new", "closed", "invalid", "recovery_required"}
	if int(c) >= len(names) {
		return "invalid"
	}
	return names[c]
}

// ExitCode gives the startup failure status for an incompatible store. Core's
// command boundary owns process exit; packages never terminate their caller.
func (c Code) ExitCode() int { return 78 }

// ControlFiles is supplied by the owner of system/core's already-held lock.
// The interface keeps canon independent of the sibling ctlfiles package.
type ControlFiles interface {
	Locked() bool
	Read(string) ([]byte, error)
	Write(string, []byte) error
}

type Migration struct {
	Version int
	SQL     string
}

type Config struct {
	Path       string
	Epoch      uint64
	Controls   ControlFiles
	Migrations []Migration
}

// Tx exposes queries but no commit, rollback or raw connection. Callbacks run
// synchronously; they must not retain Tx, start goroutines or perform external
// calls. A reader's ExecContext always returns the closed read_only code.
type Tx struct {
	tx       *sql.Tx
	readOnly bool
	ctx      context.Context
	cleanups []func()
}

func (t *Tx) operationContext(ctx context.Context) context.Context {
	if ctx == t.ctx {
		return ctx
	}
	joined, cancel := context.WithCancel(t.ctx)
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	t.cleanups = append(t.cleanups, func() { stop(); cancel() })
	return joined
}
func (t *Tx) close() {
	for _, cleanup := range t.cleanups {
		cleanup()
	}
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if t.readOnly {
		return nil, ErrReadOnly
	}
	return t.tx.ExecContext(t.operationContext(ctx), query, args...)
}
func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.operationContext(ctx), query, args...)
}
func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(t.operationContext(ctx), query, args...)
}

type request struct {
	ctx     context.Context
	fn      func(*Tx) error
	failure error
	result  chan error
}

// Store owns one writer goroutine and its dedicated connection. Reads use a
// separate mode=ro/query_only pool, including while the writer is latched.
type Store struct {
	path             string
	epoch            uint64
	controls         ControlFiles
	db               *sql.DB
	writer           *sql.Conn
	reader           *sql.DB
	readOnly, halted bool
	latch            error // owned exclusively by the writer goroutine
	requests         chan request
	done             chan struct{}
	lifetime         context.Context
	cancel           context.CancelFunc
	closeOnce        sync.Once
	readGate         sync.RWMutex
	readFailure      error // guarded by readGate
}

// Inspect reads the schema version and exact unsigned epoch before a writer
// opens. An absent database has version and epoch zero and is never created.
func Inspect(ctx context.Context, path string) (version int, epoch uint64, err error) {
	return inspect(ctx, path, -1)
}

func inspect(ctx context.Context, path string, maxVersion int) (version int, epoch uint64, err error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	db, close, err := openInspection(ctx, path)
	if err != nil {
		return 0, 0, err
	}
	defer close()
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return
	}
	if version < 0 {
		return 0, 0, ErrInvalid
	}
	if maxVersion >= 0 && version > maxVersion {
		return version, 0, nil
	}
	var exists int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='canon_meta'").Scan(&exists); err != nil || exists == 0 {
		return
	}
	var text string
	if err = db.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&text); err != nil {
		return
	}
	epoch, err = strconv.ParseUint(text, 10, 64)
	if err == nil && strconv.FormatUint(epoch, 10) != text {
		err = ErrInvalid
	}
	return
}

func PeekEpoch(ctx context.Context, path string) (uint64, error) {
	_, epoch, err := Inspect(ctx, path)
	return epoch, err
}

func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Path == "" || cfg.Epoch == 0 || cfg.Controls == nil || !cfg.Controls.Locked() {
		return nil, ErrInvalid
	}
	for i, m := range cfg.Migrations {
		if m.Version != i+1 || m.SQL == "" {
			return nil, ErrInvalid
		}
	}
	s := &Store{path: cfg.Path, epoch: cfg.Epoch, controls: cfg.Controls, requests: make(chan request), done: make(chan struct{})}
	version, _, err := inspect(ctx, cfg.Path, len(cfg.Migrations))
	if err != nil {
		return nil, s.storageError(err)
	}
	if version > len(cfg.Migrations) {
		return nil, ErrNewerSchema
	}
	s.lifetime, s.cancel = context.WithCancel(context.Background())
	marker, err := cfg.Controls.Read("halt.json")
	if err == nil {
		if err := validateHalt(marker); err != nil {
			s.cancel()
			return nil, err
		}
		s.halted = true
		s.reader, err = OpenSQLiteReader(ctx, cfg.Path)
		if err != nil {
			s.cancel()
			return nil, err
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		s.cancel()
		return nil, err
	}
	s.db, err = OpenSQLite(ctx, cfg.Path)
	if err != nil {
		s.cancel()
		return nil, s.storageError(err)
	}
	s.writer, err = s.db.Conn(ctx)
	if err != nil {
		s.cancel()
		s.db.Close()
		return nil, err
	}
	ready := make(chan error, 1)
	go s.run(ctx, cfg.Migrations, version, ready)
	if err := <-ready; err != nil {
		<-s.done
		return nil, err
	}
	return s, nil
}

func OpenReader(ctx context.Context, path string) (*Store, error) {
	db, err := OpenSQLiteReader(ctx, path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, reader: db, readOnly: true, done: make(chan struct{})}, nil
}

func (s *Store) run(ctx context.Context, migrations []Migration, version int, ready chan<- error) {
	defer close(s.done)
	defer s.cancel()
	defer s.db.Close()
	defer s.writer.Close()
	if err := s.initialize(ctx, migrations, version); err != nil {
		err = s.storageError(err)
		if !errors.Is(err, ErrWriteHalted) {
			ready <- err
			return
		}
	}
	reader, err := OpenSQLiteReader(ctx, s.path)
	if err != nil {
		ready <- s.storageError(err)
		return
	}
	s.reader = reader
	defer reader.Close()
	ready <- nil
	for {
		select {
		case <-s.lifetime.Done():
			return
		case job := <-s.requests:
			if job.failure != nil {
				job.result <- s.storageError(job.failure)
				continue
			}
			if s.latch != nil {
				job.result <- s.latch
				continue
			}
			jobCtx, cancel := context.WithCancel(job.ctx)
			stop := context.AfterFunc(s.lifetime, cancel)
			err := s.write(jobCtx, job.fn)
			stop()
			cancel()
			job.result <- s.storageError(err)
		}
	}
}

const fenceSQL = "UPDATE canon_meta SET epoch=? WHERE singleton=1 AND (length(epoch)<length(?) OR (length(epoch)=length(?) AND epoch<=?))"

func (s *Store) initialize(ctx context.Context, migrations []Migration, version int) error {
	var exists int
	if err := s.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='canon_meta'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		var tables int
		if err := s.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 || version != 0 {
			return ErrInvalid
		}
		// The first transaction installs the current epoch atomically. Subsequent
		// transactions, including each numbered migration, begin with fenceSQL.
		if _, err := s.writer.ExecContext(ctx, "CREATE TABLE canon_meta AS SELECT 1 AS singleton, CAST(? AS TEXT) AS epoch", strconv.FormatUint(s.epoch, 10)); err != nil {
			return err
		}
	}
	var check string
	if err := s.writer.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return sqlite3.CORRUPT
	}
	for _, m := range migrations[version:] {
		if err := s.transaction(ctx, func(tx *Tx) error {
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			return nil
		}, m.Version); err != nil {
			return err
		}
	}
	return s.write(ctx, func(*Tx) error { return nil })
}

func (s *Store) write(ctx context.Context, fn func(*Tx) error) error {
	return s.transaction(ctx, fn, 0)
}

func (s *Store) transaction(ctx context.Context, fn func(*Tx) error, version int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Own rollback explicitly: database/sql otherwise discards this dedicated
	// connection when the request is canceled. Statements still use ctx, and a
	// canceled request is checked again before commit.
	tx, err := s.writer.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	epoch := strconv.FormatUint(s.epoch, 10)
	result, err := tx.ExecContext(ctx, fenceSQL, epoch, epoch, epoch, epoch)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		s.latch = ErrStaleEpoch
		return ErrStaleEpoch
	}
	if err := s.authorizeCallback(true); err != nil {
		return err
	}
	view := &Tx{tx: tx, ctx: ctx}
	callbackErr := fn(view)
	view.close()
	resetErr := s.authorizeCallback(false)
	if err := errors.Join(callbackErr, resetErr); err != nil {
		return err
	}
	if version > 0 {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

// Only the writer may end a transaction or alter its fence/schema metadata.
// The authorizer sees parsed SQL, including statements in a multi-statement call.
func (s *Store) authorizeCallback(enabled bool) error {
	return s.writer.Raw(func(raw any) error {
		conn := raw.(driver.Conn).Raw()
		if !enabled {
			return conn.SetAuthorizer(nil)
		}
		return conn.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, a, b, _, _ string) sqlite3.AuthorizerReturnCode {
			if action == sqlite3.AUTH_TRANSACTION || action == sqlite3.AUTH_SAVEPOINT || action == sqlite3.AUTH_ATTACH || action == sqlite3.AUTH_DETACH {
				return sqlite3.AUTH_DENY
			}
			if action != sqlite3.AUTH_READ && (a == "canon_meta" || b == "canon_meta") {
				return sqlite3.AUTH_DENY
			}
			if action == sqlite3.AUTH_PRAGMA && b != "" {
				return sqlite3.AUTH_DENY
			}
			return sqlite3.AUTH_OK
		})
	})
}

func (s *Store) submit(ctx context.Context, fn func(*Tx) error, failure error) error {
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	if s.readOnly {
		return ErrReadOnly
	}
	if s.halted {
		return ErrWriteHalted
	}
	job := request{ctx: ctx, fn: fn, failure: failure, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	case s.requests <- job:
	}
	return <-job.result
}

func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	return s.submit(ctx, fn, nil)
}

func (s *Store) MirrorEpoch(ctx context.Context, epoch uint64) error {
	if epoch != s.epoch {
		if s.halted {
			return ErrStaleEpoch
		}
		return s.submit(ctx, nil, ErrStaleEpoch)
	}
	if s.halted {
		return nil
	}
	return s.Write(ctx, func(*Tx) error { return nil })
}

func (s *Store) Read(ctx context.Context, fn func(*Tx) error) error {
	s.readGate.RLock()
	defer s.readGate.RUnlock()
	if s.readFailure != nil {
		return errors.Join(ErrRecoveryRequired, s.readFailure)
	}
	if fn == nil {
		return ErrInvalid
	}
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	view := &Tx{tx: tx, readOnly: true, ctx: ctx}
	err = fn(view)
	view.close()
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if errors.Is(err, sqlite3.READONLY) {
		return ErrReadOnly
	}
	if (errors.Is(err, sqlite3.CORRUPT) || errors.Is(err, sqlite3.NOTADB) || errors.Is(err, sqlite3.IOERR)) && !s.readOnly && !s.halted {
		return s.submit(context.WithoutCancel(ctx), nil, err)
	}
	return err
}

func (s *Store) storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrStaleEpoch) {
		s.latch = ErrStaleEpoch
		return err
	}
	cause := ""
	switch {
	case errors.Is(err, sqlite3.CORRUPT):
		cause = "sqlite_corrupt"
	case errors.Is(err, sqlite3.NOTADB):
		cause = "sqlite_notadb"
	case errors.Is(err, sqlite3.IOERR):
		probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		probe, probeErr := OpenSQLiteReader(probeCtx, s.path)
		if probeErr == nil {
			var check string
			probeErr = probe.QueryRowContext(probeCtx, "PRAGMA quick_check").Scan(&check)
			if check != "ok" && probeErr == nil {
				probeErr = sqlite3.CORRUPT
			}
			probe.Close()
		}
		if probeErr != nil {
			cause = "sqlite_io"
		}
	}
	if cause == "" {
		return err
	}
	s.latch = ErrWriteHalted
	if _, markerErr := s.controls.Read("halt.json"); markerErr == nil {
		return errors.Join(ErrWriteHalted, err)
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return errors.Join(ErrWriteHalted, err, markerErr)
	}
	data, _ := json.Marshal(haltMarker{1, strconv.FormatUint(s.epoch, 10), cause})
	markerErr := s.controls.Write("halt.json", data)
	return errors.Join(ErrWriteHalted, err, markerErr)
}

type haltMarker struct {
	Schema int    `json:"schema"`
	Epoch  string `json:"epoch"`
	Cause  string `json:"cause"`
}

func validateHalt(data []byte) error {
	var marker haltMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return ErrInvalid
	}
	epoch, err := strconv.ParseUint(marker.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != marker.Epoch || marker.Schema != 1 {
		return ErrInvalid
	}
	if marker.Cause != "sqlite_corrupt" && marker.Cause != "sqlite_notadb" && marker.Cause != "sqlite_io" {
		return ErrInvalid
	}
	return nil
}

func (s *Store) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		if s.readOnly || s.halted {
			s.reader.Close()
			if s.cancel != nil {
				s.cancel()
			}
			close(s.done)
		} else {
			s.cancel()
		}
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
