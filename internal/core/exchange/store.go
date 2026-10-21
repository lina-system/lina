// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func exchangeID(delivery string) string {
	h := sha256.Sum256([]byte(delivery))
	return "exchange-" + hex.EncodeToString(h[:])
}
func encode(v any) []byte { b, _ := json.Marshal(v); return b }

const selectRecord = `SELECT e.id,e.delivery_id,e.from_agent,e.to_agent,e.round,e.outcome,e.needs_person,m.handoff,m.state,
 (SELECT l.recorded_at FROM exch_event x JOIN conv_event l ON l.id=x.event_id WHERE x.exchange_id=e.id ORDER BY x.id DESC LIMIT 1)
 FROM exchanges e JOIN exch_msg m ON m.delivery_id=e.delivery_id`

func get(ctx context.Context, tx *canon.Tx, id string) (Record, error) {
	var r Record
	var raw []byte
	var at string
	err := tx.QueryRowContext(ctx, selectRecord+" WHERE e.id=?", id).Scan(&r.ID, &r.DeliveryID, &r.From, &r.To, &r.Round, &r.Outcome, &r.NeedsPerson, &raw, &r.State, &at)
	if err != nil {
		return Record{}, err
	}
	if err = json.Unmarshal(raw, &r.HandOff); err != nil {
		return Record{}, err
	}
	r.Updated, err = time.Parse(time.RFC3339Nano, at)
	return r, err
}
func forDelivery(ctx context.Context, tx *canon.Tx, delivery string) (Record, error) {
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT exchange_id FROM exch_msg WHERE delivery_id=?", delivery).Scan(&id); err != nil {
		return Record{}, err
	}
	r, err := get(ctx, tx, id)
	if err == nil && r.DeliveryID != delivery {
		return Record{}, ErrClosed
	}
	return r, err
}
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	var r Record
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; r, err = get(ctx, tx, id); return err })
	if err != nil {
		return Record{}, err
	}
	return r, nil
}
func (s *Store) Start(ctx context.Context, input Request, at time.Time) (Receipt, error) {
	if !identifier(input.DeliveryID) || !identifier(input.From) || !identifier(input.To) || !timestamp(at) {
		return Receipt{}, ErrInvalid
	}
	r, material, existingReceipt, err := s.prepareInput(ctx, input, at, exchangeID(input.DeliveryID))
	if err != nil {
		return Receipt{}, err
	}
	if existingReceipt != nil {
		return *existingReceipt, nil
	}
	var receipt Receipt
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var existing, from, to string
		err := tx.QueryRowContext(ctx, `SELECT e.id,e.from_agent,e.to_agent FROM exch_msg m JOIN exchanges e ON e.id=m.exchange_id WHERE m.delivery_id=?`, r.DeliveryID).Scan(&existing, &from, &to)
		if err == nil {
			if from != r.From || to != r.To || existing != exchangeID(r.DeliveryID) {
				return ErrConflict
			}
			receipt = Receipt{ID: existing, DeliveryID: r.DeliveryID}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		r, err = normalize(r, at)
		if err != nil {
			return err
		}
		id := exchangeID(r.DeliveryID)
		if _, err = tx.ExecContext(ctx, `INSERT INTO exchanges(id,from_agent,to_agent,delivery_id,round,outcome) VALUES(?,?,?,?,1,'pending')`, id, r.From, r.To, r.DeliveryID); err != nil {
			return err
		}
		row := Record{ID: id, DeliveryID: r.DeliveryID, From: r.From, To: r.To, HandOff: r.HandOff, Round: 1, State: Queued, Outcome: Pending}
		if err = insertMessage(ctx, tx, row, at, material); err != nil {
			return err
		}
		receipt = Receipt{ID: id, DeliveryID: r.DeliveryID, Inserted: true}
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}
func insertMessage(ctx context.Context, tx *canon.Tx, r Record, at time.Time, material ledger.Material) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO exch_msg(delivery_id,exchange_id,round,handoff,state) VALUES(?,?,?,?,?)", r.DeliveryID, r.ID, r.Round, encode(r.HandOff), Queued); err != nil {
		return err
	}
	return appendEvent(ctx, tx, r, r.From, "request", string(encode(r.HandOff)), at, material)
}
func deliver(ctx context.Context, tx *canon.Tx, r Record, at time.Time) (Record, error) {
	if r.Outcome != Pending {
		return Record{}, ErrClosed
	}
	if !at.Before(r.HandOff.Deadline) {
		return Record{}, ErrClosed
	}
	if r.State != Queued {
		return r, nil
	}
	body := encode(struct {
		Speaker guard.SpeakerKind `json:"speaker_kind"`
		Agent   string            `json:"agent"`
		HandOff HandOff           `json:"handoff"`
	}{guard.Agent, r.From, r.HandOff})
	receipt, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: r.ID, Source: "exchange", NativeID: r.DeliveryID}, Body: body})
	if err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE exch_msg SET state=?,inbox_id=? WHERE delivery_id=?", Delivered, receipt.ID, r.DeliveryID); err != nil {
		return Record{}, err
	}
	if err = appendEvent(ctx, tx, r, r.To, "delivered", "", at); err != nil {
		return Record{}, err
	}
	r.State = Delivered
	return r, nil
}

// Deliver atomically admits one agent-origin input and records its delivery.
// Re-delivery after commit returns the existing mailbox row, even after a kill.
func (s *Store) Deliver(ctx context.Context, delivery string, at time.Time) (Receipt, error) {
	if !timestamp(at) {
		return Receipt{}, ErrInvalid
	}
	var receipt Receipt
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := forDelivery(ctx, tx, delivery)
		if err != nil {
			return err
		}
		inserted := r.State == Queued
		if _, err = deliver(ctx, tx, r, at); err != nil {
			return err
		}
		receipt = Receipt{ID: r.ID, DeliveryID: delivery, Inserted: inserted}
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}
func finish(ctx context.Context, tx *canon.Tx, r Record, outcome Outcome, body string, at time.Time, material ...ledger.Material) error {
	if _, err := tx.ExecContext(ctx, "UPDATE exch_msg SET state=?,answer=? WHERE delivery_id=?", Answered, body, r.DeliveryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE exchanges SET outcome=?,needs_person=0 WHERE id=?", outcome, r.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM exch_waiting WHERE exchange_id=?", r.ID); err != nil {
		return err
	}
	return appendEvent(ctx, tx, r, r.To, string(outcome), body, at, material...)
}

// Acknowledge is a terminal mailbox operation. It performs no preparation,
// background-run start, model call or answer check.
func (s *Store) Acknowledge(ctx context.Context, delivery, actor string, at time.Time) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := forDelivery(ctx, tx, delivery)
		if err != nil {
			return err
		}
		if actor != r.To {
			return ErrInvalid
		}
		if r.Outcome == Acknowledged {
			return nil
		}
		if r.Outcome != Pending {
			return ErrClosed
		}
		if _, err = deliver(ctx, tx, r, at); err != nil {
			return err
		}
		return finish(ctx, tx, r, Acknowledged, "", at)
	})
}

// Answer runs the asker's named check outside the writer, then rechecks the
// mailbox in the commit transaction. A failed or interrupted check cannot land.
func (s *Store) Answer(ctx context.Context, delivery, actor string, a Answer, at time.Time, check Check) error {
	if !timestamp(at) || check == nil {
		return ErrInvalid
	}
	var r Record
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; r, err = forDelivery(ctx, tx, delivery); return err })
	if err != nil {
		return err
	}
	if actor != r.To {
		return ErrInvalid
	}
	if r.Outcome == Checked {
		return nil
	}
	if r.Outcome != Pending {
		return ErrClosed
	}
	a, err = normalizeAnswer(a)
	if err != nil {
		return err
	}
	ok, err := check(ctx, r.From, r.HandOff.Check, a)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCheck
	}
	body := string(encode(a))
	material, err := s.stageBody(ctx, body)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := forDelivery(ctx, tx, delivery)
		if err != nil {
			return err
		}
		if current.Outcome == Checked {
			return nil
		}
		if current.Outcome != Pending {
			return ErrClosed
		}
		if _, err = deliver(ctx, tx, current, at); err != nil {
			return err
		}
		return finish(ctx, tx, current, Checked, body, at, material)
	})
}
func (s *Store) Continue(ctx context.Context, id string, input Request, at time.Time) (Receipt, error) {
	if !identifier(input.DeliveryID) || !identifier(input.From) || !identifier(input.To) || !timestamp(at) {
		return Receipt{}, ErrInvalid
	}
	input, material, existingReceipt, err := s.prepareInput(ctx, input, at, id)
	if err != nil {
		return Receipt{}, err
	}
	if existingReceipt != nil {
		return *existingReceipt, nil
	}
	var receipt Receipt
	limited := false
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.From != input.From || r.To != input.To {
			return ErrConflict
		}
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT exchange_id FROM exch_msg WHERE delivery_id=?", input.DeliveryID).Scan(&existing)
		if err == nil {
			if existing != id {
				return ErrConflict
			}
			receipt = Receipt{ID: id, DeliveryID: input.DeliveryID}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		input, err = normalize(input, at)
		if err != nil {
			return err
		}
		if r.Outcome != Checked {
			return ErrClosed
		}
		if r.Round >= RoundLimit {
			limited = true
			_, err = tx.ExecContext(ctx, "UPDATE exchanges SET outcome=? WHERE id=?", Limited, id)
			if err != nil {
				return err
			}
			return appendEvent(ctx, tx, r, r.From, string(Limited), "", at)
		}
		r.Round++
		r.DeliveryID = input.DeliveryID
		r.HandOff = input.HandOff
		if _, err = tx.ExecContext(ctx, "UPDATE exchanges SET delivery_id=?,round=?,outcome=? WHERE id=?", r.DeliveryID, r.Round, Pending, id); err != nil {
			return err
		}
		if err = insertMessage(ctx, tx, r, at, material); err != nil {
			return err
		}
		receipt = Receipt{ID: id, DeliveryID: r.DeliveryID, Inserted: true}
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	if limited {
		return Receipt{}, ErrLimit
	}
	return receipt, nil
}
