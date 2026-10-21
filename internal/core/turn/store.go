// SPDX-License-Identifier: Apache-2.0

// Package turn composes durable turn boundaries with the stateless kit loop.
package turn

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrAccepted = errors.New("turn_already_accepted")
	ErrInvalid  = errors.New("invalid_turn")
)

type State string

const (
	Active    State = "active"
	Completed State = "completed"
	Aborted   State = "aborted"
	Failed    State = "failed"
)

// Schema is installed by the owner in a numbered canon migration, after
// canon.InboxSchema. The turn never opens a second writer or migrates on demand.
const Schema = `
CREATE TABLE turn_runs (
 id TEXT PRIMARY KEY NOT NULL,
 conversation TEXT NOT NULL,
 inbox_id INTEGER NOT NULL UNIQUE REFERENCES inbox(id),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 state TEXT NOT NULL CHECK(state IN ('active','completed','aborted','failed'))
);
CREATE TABLE turn_items (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, item BLOB NOT NULL,
 PRIMARY KEY(turn_id,round,ordinal)
);
CREATE TABLE turn_calls (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, call_id TEXT NOT NULL, call BLOB NOT NULL, result BLOB,
 PRIMARY KEY(turn_id,round,ordinal), UNIQUE(turn_id,call_id)
);
CREATE TABLE turn_partials (
 turn_id TEXT NOT NULL REFERENCES turn_runs(id), round INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, body TEXT NOT NULL,
 PRIMARY KEY(turn_id,round,ordinal)
);
`

type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}
type Store struct{ db Canon }

func New(db Canon) *Store { return &Store{db: db} }

type Record struct {
	ID, Conversation string
	InboxID          int64
	Prepared         bool
	State            State
	Items            []provider.Item // verbatim evidence; not an instruction to replay
	Results          []loop.Result   // ordered by round and call ordinal
}

func (s *Store) Claim(ctx context.Context, id, conversation string, inboxID int64) error {
	return s.claim(ctx, id, conversation, inboxID, "")
}

func (s *Store) claim(ctx context.Context, id, conversation string, inboxID int64, inputEvent string) error {
	if id == "" || conversation == "" || inboxID <= 0 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_runs WHERE id=? OR inbox_id=?", id, inboxID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrAccepted
		}
		var owner string
		if err := tx.QueryRowContext(ctx, "SELECT conversation FROM inbox WHERE id=?", inboxID).Scan(&owner); err != nil {
			return err
		}
		if owner != conversation {
			return ErrInvalid
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES(?,?,?,?)", id, conversation, inboxID, Active)
		if err != nil {
			return err
		}
		return claimContext(ctx, tx, id, conversation, inputEvent)
	})
}
func active(ctx context.Context, tx *canon.Tx, id string) error {
	var state State
	if err := tx.QueryRowContext(ctx, "SELECT state FROM turn_runs WHERE id=?", id).Scan(&state); err != nil {
		return err
	}
	if state != Active {
		return ErrAccepted
	}
	return nil
}
func (s *Store) Prepared(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := active(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE turn_runs SET prepared=1 WHERE id=?", id)
		return err
	})
}
func (s *Store) Journal(id string) loop.Journal {
	write := func(ctx context.Context, fn func(*canon.Tx) error) error {
		return s.db.Write(ctx, func(tx *canon.Tx) error {
			if err := active(ctx, tx, id); err != nil {
				return err
			}
			return fn(tx)
		})
	}
	return loop.Journal{
		Item: func(ctx context.Context, round int, index int64, item provider.Item) error {
			return write(ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO turn_items(turn_id,round,ordinal,item) VALUES(?,?,?,?)", id, round, index, []byte(item))
				return err
			})
		},
		Calls: func(ctx context.Context, round int, calls []provider.ToolCall) error {
			return write(ctx, func(tx *canon.Tx) error {
				for i, c := range calls {
					b, err := json.Marshal(c)
					if err != nil {
						return err
					}
					if _, err = tx.ExecContext(ctx, "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call) VALUES(?,?,?,?,?)", id, round, i, c.ID, b); err != nil {
						return err
					}
				}
				return nil
			})
		},
		Result: func(ctx context.Context, round, index int, r loop.Result) error {
			return write(ctx, func(tx *canon.Tx) error {
				b, err := json.Marshal(r)
				if err != nil {
					return err
				}
				res, err := tx.ExecContext(ctx, "UPDATE turn_calls SET result=? WHERE turn_id=? AND round=? AND ordinal=? AND call_id=? AND result IS NULL", b, id, round, index, r.ID)
				if err != nil {
					return err
				}
				n, err := res.RowsAffected()
				if err == nil && n != 1 {
					return ErrInvalid
				}
				return err
			})
		},
		Partial: func(ctx context.Context, round int, index int64, text string) error {
			return write(ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO turn_partials(turn_id,round,ordinal,body) VALUES(?,?,?,?)", id, round, index, text)
				return err
			})
		},
	}
}

// Recover runs once at startup, after authority recovery and before admission.
// Accepted turns are terminalized, never reissued to a model or tool. Receipts
// remain intact; unresolved effects are owned by authority's unknown state.
func (s *Store) Recover(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := recoverNotices(ctx, tx); err != nil {
			return err
		}
		if err := recoverItems(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT c.turn_id,c.round,c.ordinal,c.call_id FROM turn_calls c JOIN turn_runs r ON r.id=c.turn_id WHERE c.result IS NULL`)
		if err != nil {
			return err
		}
		type pending struct {
			id, call     string
			round, index int
		}
		var calls []pending
		for rows.Next() {
			var p pending
			if err = rows.Scan(&p.id, &p.round, &p.index, &p.call); err != nil {
				rows.Close()
				return err
			}
			calls = append(calls, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, p := range calls {
			b, _ := json.Marshal(loop.Result{ID: p.call, Code: loop.Aborted})
			if _, err = tx.ExecContext(ctx, "UPDATE turn_calls SET result=? WHERE turn_id=? AND round=? AND ordinal=?", b, p.id, p.round, p.index); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE turn_runs SET state=? WHERE state=?", Aborted, Active)
		return err
	})
}
func (s *Store) Read(ctx context.Context, id string) (Record, error) {
	r := Record{ID: id}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT conversation,inbox_id,prepared,state FROM turn_runs WHERE id=?", id).Scan(&r.Conversation, &r.InboxID, &r.Prepared, &r.State); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT item FROM turn_items WHERE turn_id=? ORDER BY round,ordinal", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return err
			}
			r.Items = append(r.Items, b)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, "SELECT result FROM turn_calls WHERE turn_id=? AND result IS NOT NULL ORDER BY round,ordinal", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				return err
			}
			var result loop.Result
			if err = json.Unmarshal(b, &result); err != nil {
				return err
			}
			r.Results = append(r.Results, result)
		}
		return rows.Err()
	})
	return r, err
}

type Options struct {
	ID, Conversation string
	InboxID          int64
	// InputEvent binds the accepted input's ledger identity for durable stop
	// and restart notices. The owner installs ControlSchema before use.
	InputEvent string
	Prepare    func(context.Context) error
	// Preparation is the complete four-step conversation path. Prepare is the
	// low-level durability hook; supplying both is invalid.
	Preparation *Preparation
	Loop        loop.Config
}

func (s *Store) Run(ctx context.Context, o Options) (out loop.Outcome, err error) {
	// Preparation and claim failures occur before loop.Run owns the queue.
	// Close is idempotent, so this also preserves its normal handoff.
	defer func() {
		if o.Loop.Inputs != nil {
			out.FollowUps = append(out.FollowUps, o.Loop.Inputs.Close()...)
		}
		if ctx.Err() != nil {
			err = errors.Join(err, context.Cause(ctx))
		}
	}()
	if err = s.claim(ctx, o.ID, o.Conversation, o.InboxID, o.InputEvent); err != nil {
		return out, err
	}
	defer func() {
		state := Completed
		if err != nil {
			state = Failed
		}
		if ctx.Err() != nil {
			state = Aborted
		}
		save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, s.db.Write(save, func(tx *canon.Tx) error {
			if e := active(save, tx, o.ID); e != nil {
				return e
			}
			if state == Aborted {
				cause := Interrupt{Issuer: Shutdown}
				var typed Interrupt
				if errors.As(context.Cause(ctx), &typed) {
					cause = typed
				} else if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
					cause.Issuer = Watchdog
				}
				if e := interruptNotice(save, tx, o.ID, cause); e != nil {
					return e
				}
			}
			_, e := tx.ExecContext(save, "UPDATE turn_runs SET state=? WHERE id=?", state, o.ID)
			return e
		}))
	}()
	cfg := o.Loop
	if o.Preparation != nil {
		if o.Prepare != nil {
			return out, o.Preparation.failure(ContextStep, ErrInvalid)
		}
		cfg, err = o.Preparation.prepare(ctx, o.ID, cfg)
		if err != nil {
			return out, err
		}
	} else if o.Prepare != nil {
		if err = o.Prepare(ctx); err != nil {
			return out, err
		}
	}
	if err = s.Prepared(ctx, o.ID); err != nil {
		if o.Preparation != nil {
			err = o.Preparation.failure(ContextStep, err)
		}
		return out, err
	}
	cfg.Journal = s.Journal(o.ID)
	return loop.Run(ctx, cfg)
}
