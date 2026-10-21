// SPDX-License-Identifier: Apache-2.0

// Package eventstore owns diagnosis persistence independently of canon. The
// caller supplies the SQLite database/sql driver selected by packaging.
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/thisisjun786/lina/kit/diag"
)

var (
	ErrStorage  = errors.New("diagnosis storage unavailable")
	ErrClosed   = errors.New("diagnosis store closed")
	ErrNotFound = errors.New("diagnosis event not found")
)

// A private backend keeps policy tests independent of a particular SQL driver.
// Production always uses sqlBackend; successful append means a committed record.
type backend interface {
	all(context.Context) ([]diag.Event, error)
	append(context.Context, diag.Event) error
	close() error
}

type Store struct {
	mu      sync.Mutex
	backend backend
	reserve *Reserve
	isFull  func(error) bool
	closed  bool
}

// Open dedicates one connection to this writer. db must be an exclusively owned
// SQLite database at system/events/events.sqlite, never canon or a backup file.
// The caller owns db and reserve and closes them after Store.Close. isFull must
// recognize the binding's typed SQLITE_FULL error, never match an error string.
// A consumed reserve does not prevent opening or reading the committed events.
// Check reserve.Present and replenish explicitly after disk space is recovered.
func Open(ctx context.Context, db *sql.DB, reserve *Reserve, isFull func(error) bool) (*Store, error) {
	if db == nil || reserve == nil || isFull == nil {
		return nil, diag.ErrInvalid
	}
	if err := reserve.inspect(); err != nil {
		return nil, ErrStorage
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, ErrStorage
	}
	b := &sqlBackend{conn: conn}
	if err := b.initialize(ctx); err != nil {
		conn.Close()
		return nil, ErrStorage
	}
	s := &Store{backend: b, reserve: reserve, isFull: isFull}
	if _, err := s.read(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// Append validates the complete existing graph before adding one event. Roots
// are explicit boot/turn/task events; a child can only point to a committed id.
// All events commit synchronously, including failure/unknown/denied/cancelled.
func (s *Store) Append(ctx context.Context, e diag.Event) error {
	ctx, cancel := diag.CommitContext(ctx, e.Result)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	events, err := s.read(ctx)
	if err != nil {
		return err
	}
	byID, err := index(events)
	if err != nil {
		return err
	}
	if _, exists := byID[e.EventID]; exists {
		return diag.ErrInvalid
	}
	if !e.Cause.ParentEventID.IsZero() {
		if _, exists := byID[e.Cause.ParentEventID]; !exists {
			return diag.ErrInvalid
		}
	}
	err = s.backend.append(ctx, e)
	// A full-disk transaction has failed; release actual preallocated disk space
	// and retry the same id once, even if the reserve was already consumed. The
	// SQL backend recognizes a previously committed identical id, so an ambiguous
	// commit cannot duplicate a diagnosis event.
	emergency := e.Result.Terminal()
	if err != nil && emergency && s.isFull != nil && s.isFull(err) && s.reserve != nil {
		if releaseErr := s.reserve.Release(); releaseErr != nil {
			return ErrStorage
		}
		err = s.backend.append(ctx, e)
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}

func (s *Store) read(ctx context.Context) ([]diag.Event, error) {
	events, err := s.backend.all(ctx)
	if err != nil {
		return nil, ErrStorage
	}
	if _, err := index(events); err != nil {
		return nil, err
	}
	return events, nil
}

// index validates links in linear time, rejecting cycles and duplicate ids.
func index(events []diag.Event) (map[diag.ID]diag.Event, error) {
	byID := make(map[diag.ID]diag.Event, len(events))
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		if _, exists := byID[e.EventID]; exists {
			return nil, diag.ErrInvalid
		}
		byID[e.EventID] = e
	}
	done := make(map[diag.ID]bool, len(events))
	for _, e := range events {
		path := make(map[diag.ID]bool)
		for !done[e.EventID] {
			if path[e.EventID] {
				return nil, diag.ErrInvalid
			}
			path[e.EventID] = true
			if e.Cause.ParentEventID.IsZero() {
				break
			}
			var ok bool
			e, ok = byID[e.Cause.ParentEventID]
			if !ok {
				return nil, diag.ErrInvalid
			}
		}
		for id := range path {
			done[id] = true
		}
	}
	return byID, nil
}

// Chain returns the complete ancestry, root first, independently of filters.
// Query is a filter, not an authorization boundary; callers enforce agent access.
func (s *Store) Chain(ctx context.Context, id diag.ID) ([]diag.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	byID, err := index(events)
	if err != nil {
		return nil, err
	}
	e, ok := byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	chain := []diag.Event{e}
	for !e.Cause.ParentEventID.IsZero() {
		e = byID[e.Cause.ParentEventID]
		chain = append(chain, e)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.backend.close(); err != nil {
		return ErrStorage
	}
	return nil
}

type sqlBackend struct{ conn *sql.Conn }

const schema = `CREATE TABLE IF NOT EXISTS events (
 event_id TEXT PRIMARY KEY NOT NULL,
 record TEXT NOT NULL
) WITHOUT ROWID`

func (b *sqlBackend) initialize(ctx context.Context) error {
	// The database is a dedicated event store. FULL applies to this connection,
	// not to whichever pooled connection happens to execute a later write.
	var mode string
	if err := b.conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return ErrStorage
	}
	if _, err := b.conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	var synchronous int
	if err := b.conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous != 2 {
		return ErrStorage
	}
	_, err := b.conn.ExecContext(ctx, schema)
	return err
}
func (b *sqlBackend) all(ctx context.Context) ([]diag.Event, error) {
	rows, err := b.conn.QueryContext(ctx, "SELECT event_id, record FROM events")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []diag.Event{}
	for rows.Next() {
		var id string
		var record []byte
		if err := rows.Scan(&id, &record); err != nil {
			return nil, err
		}
		var event diag.Event
		if err := json.Unmarshal(record, &event); err != nil {
			return nil, err
		}
		if event.EventID.String() != id {
			return nil, diag.ErrInvalid
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
func (b *sqlBackend) append(ctx context.Context, event diag.Event) error {
	record, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := b.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Resolve a retry after an ambiguous commit by content as well as identity.
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT record FROM events WHERE event_id = ?", event.EventID.String()).Scan(&existing)
	if err == nil {
		if string(existing) != string(record) {
			return diag.ErrInvalid
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO events (event_id, record) VALUES (?, ?)", event.EventID.String(), string(record)); err != nil {
		return err
	}
	return tx.Commit()
}
func (b *sqlBackend) close() error { return b.conn.Close() }
