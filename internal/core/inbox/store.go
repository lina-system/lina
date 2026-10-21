// SPDX-License-Identifier: Apache-2.0

// Package inbox owns durable input admission through canon's fenced writer.
package inbox

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid  = errors.New("invalid inbox key")
	ErrNotFound = sql.ErrNoRows
)

// Key identifies one delivery. Its fields are opaque and case sensitive.
type Key struct {
	Conversation string
	Source       string
	NativeID     string
}

func (k Key) valid() bool { return k.Conversation != "" && k.Source != "" && k.NativeID != "" }

type Input struct {
	Key
	Body []byte
}

type Record struct {
	ID int64
	Input
}

// Receipt acknowledges committed storage, not completion of a turn. A duplicate
// returns the original ID with Inserted false, even if its body differs.
type Receipt struct {
	ID       int64
	Inserted bool
}

// Canon is the transaction boundary. Write must return nil only after commit;
// callbacks and transaction handles must obey canon.Store's lifetime rules.
type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

type Store struct{ db Canon }

// New uses an existing canon store whose migrations include canon.InboxSchema.
// The caller owns the store and closes it after inbox users have stopped.
func New(db Canon) *Store { return &Store{db: db} }

// Admit returns a receipt only after the fenced transaction commits. Failure,
// including cancellation or an error at COMMIT, always returns a zero receipt.
// Repeated delivery never overwrites the original body or starts another turn.
func (s *Store) Admit(ctx context.Context, input Input) (Receipt, error) {
	if !input.Key.valid() {
		return Receipt{}, ErrInvalid
	}
	var receipt Receipt
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		receipt, err = Insert(ctx, tx, input)
		return err
	})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Insert composes admission with other writes (such as a channel cursor) in
// the caller's canon transaction. Its receipt is provisional: the caller must
// not acknowledge or dispatch it until the enclosing Write returns nil.
func Insert(ctx context.Context, tx *canon.Tx, input Input) (Receipt, error) {
	if !input.Key.valid() {
		return Receipt{}, ErrInvalid
	}
	body := input.Body
	if body == nil {
		body = []byte{}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO inbox(conversation, source, native_id, body)
		VALUES (?, ?, ?, ?) ON CONFLICT(conversation, source, native_id) DO NOTHING`,
		input.Conversation, input.Source, input.NativeID, body)
	if err != nil {
		return Receipt{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Receipt{}, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM inbox WHERE conversation=? AND source=? AND native_id=?`,
		input.Conversation, input.Source, input.NativeID).Scan(&id)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{ID: id, Inserted: changed == 1}, nil
}

// Get reads committed input, including after a process restart. It never
// claims or replays a turn; recovery and turn claims belong to their own flow.
func (s *Store) Get(ctx context.Context, key Key) (Record, error) {
	if !key.valid() {
		return Record{}, ErrInvalid
	}
	record := Record{Input: Input{Key: key}}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id, body FROM inbox WHERE conversation=? AND source=? AND native_id=?`,
			key.Conversation, key.Source, key.NativeID).Scan(&record.ID, &record.Body)
	})
	if err != nil {
		return Record{}, err
	}
	return record, nil
}
