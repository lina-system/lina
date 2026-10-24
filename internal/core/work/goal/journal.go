// SPDX-License-Identifier: Apache-2.0

// Package work owns the durable work engine and derives actions from committed
// state. Its adapters execute effects outside the canon writer transaction.
package goal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid  = errors.New("invalid_goal_transition")
	ErrConflict = errors.New("work_transition_conflict")
)

type Canon interface {
	Read(context.Context, func(*canon.Tx) error) error
	Write(context.Context, func(*canon.Tx) error) error
}

type Transition struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	At   int64           `json:"at"`
	Data json.RawMessage `json:"data,omitempty"`
}

type Record struct {
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Version int64           `json:"version"`
	Body    json.RawMessage `json:"body"`
}

type entry struct {
	Event   Transition `json:"event"`
	Changes []Record   `json:"changes"`
}

type Journal struct {
	db   Canon
	wake chan struct{}
}

func NewJournal(db Canon) *Journal       { return &Journal{db: db, wake: make(chan struct{}, 1)} }
func (j *Journal) Wake() <-chan struct{} { return j.wake }
func (j *Journal) notify() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

func key(kind, id string) string { return kind + "\x00" + id }
func validKey(kind, id string) bool {
	return (kind == "goal" || kind == "node" || kind == "action" || kind == "limit") && strings.TrimSpace(id) != "" && !strings.ContainsRune(id, 0)
}
func copyRecord(r Record) Record { r.Body = bytes.Clone(r.Body); return r }

// View is transaction-local. Get and Records return copies; only Put changes
// the projection. Callers may not retain View or make external calls in Update.
type View struct {
	records map[string]Record
	changes map[string]Record
}

func (v *View) Get(kind, id string) (Record, bool) {
	r, ok := v.records[key(kind, id)]
	return copyRecord(r), ok
}
func (v *View) Records(kind string) []Record {
	var out []Record
	for _, r := range v.records {
		if r.Kind == kind {
			out = append(out, copyRecord(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (v *View) Put(kind, id string, body json.RawMessage) error {
	if !validKey(kind, id) || !json.Valid(body) {
		return ErrInvalid
	}
	k := key(kind, id)
	old := v.records[k]
	if bytes.Equal(old.Body, body) {
		return nil
	}
	version := old.Version + 1
	if changed, ok := v.changes[k]; ok {
		version = changed.Version
	}
	r := Record{Kind: kind, ID: id, Version: version, Body: bytes.Clone(body)}
	v.records[k], v.changes[k] = r, r
	return nil
}

func readView(ctx context.Context, tx *canon.Tx) (*View, error) {
	rows, err := tx.QueryContext(ctx, "SELECT kind,id,version,body FROM work_records ORDER BY kind,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	v := &View{records: map[string]Record{}, changes: map[string]Record{}}
	for rows.Next() {
		var r Record
		if err = rows.Scan(&r.Kind, &r.ID, &r.Version, &r.Body); err != nil {
			return nil, err
		}
		v.records[key(r.Kind, r.ID)] = r
	}
	return v, rows.Err()
}

// Update atomically journals the source event and its resulting projections.
// Same-id replay runs no callback; a different event reusing the id is refused.
// The wake is sent only after commit. Startup and the sweep recover lost wakes.
func (j *Journal) Update(ctx context.Context, event Transition, apply func(*View) error) error {
	if j.db == nil || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.Kind) == "" || event.At <= 0 || len(event.Data) > 0 && !json.Valid(event.Data) || apply == nil {
		return ErrInvalid
	}
	input, err := json.Marshal(event)
	if err != nil {
		return err
	}
	committed := false
	err = j.db.Write(ctx, func(tx *canon.Tx) error {
		var previous []byte
		err := tx.QueryRowContext(ctx, "SELECT body FROM work_events WHERE id=?", event.ID).Scan(&previous)
		if err == nil {
			var saved entry
			if json.Unmarshal(previous, &saved) != nil {
				return ErrInvalid
			}
			original, e := json.Marshal(saved.Event)
			if e != nil {
				return e
			}
			if !bytes.Equal(input, original) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		v, err := readView(ctx, tx)
		if err != nil {
			return err
		}
		if err = apply(v); err != nil {
			return err
		}
		changes := make([]Record, 0, len(v.changes))
		for _, r := range v.changes {
			changes = append(changes, r)
		}
		sort.Slice(changes, func(i, k int) bool { return key(changes[i].Kind, changes[i].ID) < key(changes[k].Kind, changes[k].ID) })
		body, err := json.Marshal(entry{Event: event, Changes: changes})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO work_events(id,kind,at,body) VALUES(?,?,?,?)", event.ID, event.Kind, event.At, body); err != nil {
			return err
		}
		for _, r := range changes {
			if err = putRecord(ctx, tx, r); err != nil {
				return err
			}
		}
		committed = true
		return nil
	})
	if err == nil && committed {
		j.notify()
	}
	return err
}
func putRecord(ctx context.Context, tx *canon.Tx, r Record) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO work_records(kind,id,version,body) VALUES(?,?,?,?)
 ON CONFLICT(kind,id) DO UPDATE SET version=excluded.version,body=excluded.body`, r.Kind, r.ID, r.Version, r.Body)
	return err
}
func (j *Journal) Records(ctx context.Context, kind string) ([]Record, error) {
	var records []Record
	err := j.db.Read(ctx, func(tx *canon.Tx) error {
		v, err := readView(ctx, tx)
		if err == nil {
			records = v.Records(kind)
		}
		return err
	})
	return records, err
}

// Rebuild restores projections from committed transitions, retaining evidence
// and action claims. It is maintenance, never a reason to re-execute an effect.
func (j *Journal) Rebuild(ctx context.Context) error {
	return j.db.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,kind,at,body FROM work_events ORDER BY seq")
		if err != nil {
			return err
		}
		records := map[string]Record{}
		for rows.Next() {
			var id, kind string
			var at int64
			var raw []byte
			if err = rows.Scan(&id, &kind, &at, &raw); err != nil {
				rows.Close()
				return err
			}
			var e entry
			if json.Unmarshal(raw, &e) != nil || e.Event.ID != id || e.Event.Kind != kind || e.Event.At != at {
				rows.Close()
				return ErrInvalid
			}
			for _, r := range e.Changes {
				k := key(r.Kind, r.ID)
				if !validKey(r.Kind, r.ID) || !json.Valid(r.Body) || r.Version != records[k].Version+1 {
					rows.Close()
					return ErrInvalid
				}
				records[k] = r
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM work_records"); err != nil {
			return err
		}
		for _, r := range records {
			if err = putRecord(ctx, tx, r); err != nil {
				return err
			}
		}
		return nil
	})
}
