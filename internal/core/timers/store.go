// SPDX-License-Identifier: Apache-2.0
// Package timers owns durable schedules, fire claims and sensor signatures.
package timers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrConflict = errors.New("timer conflict")
	ErrNotFound = errors.New("timer not found")
	ErrClaim    = errors.New("timer claim changed")
)

const stamp = "2006-01-02T15:04:05.000000000Z"

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(stamp)
}

type Action struct {
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Record string `json:"record,omitempty"`
}
type Timer struct {
	ID           string   `json:"id"`
	Person       string   `json:"person"`
	Agent        string   `json:"agent"`
	Conversation string   `json:"conversation"`
	Schedule     Schedule `json:"schedule"`
	Action       Action   `json:"action"`
}
type Status struct {
	Timer   Timer        `json:"timer"`
	Next    string       `json:"next"`
	Enabled bool         `json:"enabled"`
	Fires   []FireStatus `json:"fires"`
}
type FireStatus struct {
	ID      string `json:"id"`
	Instant string `json:"instant"`
	State   string `json:"state"`
}
type Fire struct {
	ID      string
	Timer   Timer
	Instant time.Time
	Epoch   string
	Attempt int
}
type Store struct{ db *canon.Store }

func New(db *canon.Store) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, t Timer, now time.Time) (Timer, error) {
	if t.ID == "" || len(t.ID) > 512 || t.Person == "" || t.Agent == "" || t.Conversation == "" || t.Action.Text == "" || len(t.Action.Text) > 8192 {
		return Timer{}, ErrInvalid
	}
	switch t.Action.Kind {
	case "reminder", "job":
	case "sensor":
		if t.Action.Record == "" {
			return Timer{}, ErrInvalid
		}
	default:
		return Timer{}, ErrInvalid
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return Timer{}, err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var saved []byte
		e := tx.QueryRowContext(ctx, "SELECT authored FROM timer WHERE id=?", t.ID).Scan(&saved)
		if e == nil {
			if string(saved) != string(raw) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		next, e := t.Schedule.Next(now)
		if e != nil {
			return e
		}
		if !t.Schedule.At.IsZero() {
			next = t.Schedule.At
		}
		if next.IsZero() {
			return ErrInvalid
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO timer(id,person,agent,conversation,authored,next_at) VALUES(?,?,?,?,?,?)", t.ID, t.Person, t.Agent, t.Conversation, raw, timestamp(next))
		return e
	})
	if err != nil {
		return Timer{}, err
	}
	return t, nil
}
func (s *Store) Get(ctx context.Context, id, person, agent, conversation string) (Status, error) {
	var out Status
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		err := tx.QueryRowContext(ctx, "SELECT authored,next_at,enabled FROM timer WHERE id=? AND person=? AND agent=? AND conversation=?", id, person, agent, conversation).Scan(&raw, &out.Next, &out.Enabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &out.Timer); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id,instant,state FROM timer_fire WHERE timer_id=? ORDER BY instant DESC LIMIT 20", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Fires = []FireStatus{}
		for rows.Next() {
			var f FireStatus
			if err = rows.Scan(&f.ID, &f.Instant, &f.State); err != nil {
				return err
			}
			out.Fires = append(out.Fires, f)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Store) Cancel(ctx context.Context, id, person, agent, conversation string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE timer SET enabled=0,next_at='' WHERE id=? AND person=? AND agent=? AND conversation=?", id, person, agent, conversation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, "UPDATE timer_fire SET state='cancelled' WHERE timer_id=? AND state='pending'", id)
		return err
	})
}

// ClaimDue commits selection, one unique fire per instant and advancement in
// one transaction. No caller may dispatch a provisional fire before it returns.
func (s *Store) ClaimDue(ctx context.Context, now time.Time, epoch string) ([]Fire, error) {
	var fires []Fire
	err := s.db.Write(ctx, func(tx *canon.Tx) error { var err error; fires, err = claimIn(ctx, tx, now, epoch); return err })
	if err != nil {
		return nil, err
	}
	return fires, nil
}
func claimIn(ctx context.Context, tx *canon.Tx, now time.Time, epoch string) ([]Fire, error) {
	if epoch == "" {
		return nil, ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT authored,next_at FROM timer WHERE enabled=1 AND next_at<>'' AND next_at<=? AND NOT EXISTS(SELECT 1 FROM timer_fire f WHERE f.timer_id=timer.id AND f.state IN ('pending','claimed')) ORDER BY next_at,id LIMIT 64", timestamp(now))
	if err != nil {
		return nil, err
	}
	type due struct {
		raw []byte
		at  string
	}
	dueTimers := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.raw, &d.at); err != nil {
			rows.Close()
			return nil, err
		}
		dueTimers = append(dueTimers, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, d := range dueTimers {
		var t Timer
		if err = json.Unmarshal(d.raw, &t); err != nil {
			return nil, err
		}
		at, e := time.Parse(stamp, d.at)
		if e != nil {
			return nil, e
		}
		next, e := t.Schedule.Next(at)
		if e != nil {
			return nil, e
		}
		hash := sha256.Sum256([]byte(t.ID + "\x00" + d.at))
		id := hex.EncodeToString(hash[:])
		_, err = tx.ExecContext(ctx, "INSERT INTO timer_fire(id,timer_id,instant,authored,read_only,state) VALUES(?,?,?,?,?,'pending') ON CONFLICT(timer_id,instant) DO NOTHING", id, t.ID, d.at, d.raw, t.Action.Kind != "job")
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE timer SET next_at=? WHERE id=?", timestamp(next), t.ID); err != nil {
			return nil, err
		}
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,authored,instant,attempts FROM timer_fire WHERE state='pending' ORDER BY instant,id LIMIT 64")
	if err != nil {
		return nil, err
	}
	fires := []Fire{}
	for rows.Next() {
		var f Fire
		var raw []byte
		var at string
		if err = rows.Scan(&f.ID, &raw, &at, &f.Attempt); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &f.Timer); err != nil {
			break
		}
		f.Instant, err = time.Parse(stamp, at)
		if err != nil {
			break
		}
		f.Epoch = epoch
		f.Attempt++
		fires = append(fires, f)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	for _, f := range fires {
		if _, err = tx.ExecContext(ctx, "UPDATE timer_fire SET state='claimed',epoch=?,attempts=? WHERE id=?", epoch, f.Attempt, f.ID); err != nil {
			return nil, err
		}
	}
	return fires, nil
}

// Recover replaces an abandoned read-only action once. An effect-capable job
// or an already replaced claim remains visible as unknown, never replayed.
func (s *Store) Recover(ctx context.Context, epoch string) error {
	if epoch == "" {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE timer_fire SET state=CASE WHEN read_only=1 AND attempts=1 THEN 'pending' ELSE 'unknown' END WHERE state='claimed' AND epoch<>?`, epoch)
		return err
	})
}
func checkClaim(ctx context.Context, tx *canon.Tx, f Fire) error {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM timer_fire WHERE id=? AND state='claimed' AND epoch=? AND attempts=?", f.ID, f.Epoch, f.Attempt).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrClaim
	}
	return nil
}

// Observe freezes the sampled signature and its change decision atomically.
// A recovered claim retains that decision even if the current source changed.
func (s *Store) Observe(ctx context.Context, f Fire, signature string) (bool, error) {
	if f.Timer.Action.Kind != "sensor" || signature == "" {
		return false, ErrInvalid
	}
	var changed bool
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := checkClaim(ctx, tx, f); err != nil {
			return err
		}
		var sampled bool
		if err := tx.QueryRowContext(ctx, "SELECT sampled,changed FROM timer_fire WHERE id=?", f.ID).Scan(&sampled, &changed); err != nil {
			return err
		}
		if sampled {
			return nil
		}
		var old string
		var initialized bool
		if err := tx.QueryRowContext(ctx, "SELECT signature,initialized FROM timer WHERE id=?", f.Timer.ID).Scan(&old, &initialized); err != nil {
			return err
		}
		changed = !initialized || old != signature
		if _, err := tx.ExecContext(ctx, "UPDATE timer SET signature=?,initialized=1 WHERE id=?", signature, f.Timer.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE timer_fire SET sampled=1,changed=?,signature=? WHERE id=?", changed, signature, f.ID)
		return err
	})
	return changed, err
}
func (s *Store) Finish(ctx context.Context, f Fire) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := checkClaim(ctx, tx, f); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE timer_fire SET state='handled' WHERE id=?", f.ID)
		return err
	})
}

// Fail settles a failed delivery under the same bounded replay policy as
// restart recovery. The durable attempt budget is shared across both paths.
func (s *Store) Fail(ctx context.Context, f Fire) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := checkClaim(ctx, tx, f); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE timer_fire SET state=CASE
			WHEN read_only=0 OR attempts>1 THEN 'unknown'
			WHEN NOT EXISTS(SELECT 1 FROM timer WHERE id=timer_id AND enabled=1) THEN 'cancelled'
			ELSE 'pending' END WHERE id=?`, f.ID)
		return err
	})
}

func (s *Store) NextDue(ctx context.Context) (time.Time, error) {
	var next sql.NullString
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT min(next_at) FROM timer WHERE enabled=1 AND next_at<>'' AND NOT EXISTS(SELECT 1 FROM timer_fire f WHERE f.timer_id=timer.id AND f.state IN ('pending','claimed'))").Scan(&next)
	})
	if err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Parse(stamp, next.String)
}
