// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Origin is injected by Core, never decoded from the hand-off's model arguments.
type Origin struct {
	Person, Conversation string
	CheckBack            bool
	NextCheck            time.Time
	Artifact             Artifact
}
type Artifact struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type Status struct {
	ID         string    `json:"id"`
	DeliveryID string    `json:"delivery_id"`
	To         string    `json:"to"`
	Delivery   string    `json:"delivery"`
	Waiting    string    `json:"waiting"`
	Outcome    Outcome   `json:"outcome"`
	NextCheck  time.Time `json:"next_check"`
	Report     *Answer   `json:"report,omitempty"`
}
type Notice struct {
	ID, DeliveryID, ExchangeID, Person, Agent, Conversation, Reason string
}

func insertOrigin(ctx context.Context, tx *canon.Tx, r Request, at time.Time) error {
	o := r.Origin
	if !identifier(o.Person) || !identifier(o.Conversation) || !timestamp(o.NextCheck) || !o.NextCheck.After(at) {
		return ErrInvalid
	}
	if (o.Artifact != Artifact{}) && (!identifier(o.Artifact.Kind) || !identifier(o.Artifact.ID)) {
		return ErrInvalid
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO exch_origins(delivery_id,person,conversation,check_back,next_check,artifact_kind,artifact_id) VALUES(?,?,?,?,?,?,?)`, r.DeliveryID, o.Person, o.Conversation, o.CheckBack, o.NextCheck.UnixNano(), o.Artifact.Kind, o.Artifact.ID)
	return err
}
func (s *Store) Origin(ctx context.Context, delivery string) (Origin, error) {
	var o Origin
	var due int64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT person,conversation,check_back,next_check,artifact_kind,artifact_id FROM exch_origins WHERE delivery_id=?`, delivery).Scan(&o.Person, &o.Conversation, &o.CheckBack, &due, &o.Artifact.Kind, &o.Artifact.ID)
	})
	o.NextCheck = time.Unix(0, due).UTC()
	return o, err
}
func (s *Store) Status(ctx context.Context, id, person, agent string) (Status, error) {
	var out Status
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		r, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		var p string
		var due int64
		var check bool
		var body []byte
		if err = tx.QueryRowContext(ctx, `SELECT o.person,o.next_check,o.check_back,m.answer FROM exch_origins o JOIN exch_msg m USING(delivery_id) WHERE o.delivery_id=?`, r.DeliveryID).Scan(&p, &due, &check, &body); err != nil {
			return err
		}
		if p != person || r.From != agent {
			return ErrInvalid
		}
		out = Status{ID: r.ID, DeliveryID: r.DeliveryID, To: r.To, Delivery: "pending", Waiting: "off", Outcome: r.Outcome, NextCheck: time.Unix(0, due).UTC()}
		// Expiry is not delivery. Only the receiver's committed event is a receipt.
		var receipts int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM exch_event WHERE delivery_id=? AND kind='delivered'`, r.DeliveryID).Scan(&receipts); err != nil {
			return err
		}
		if receipts > 0 {
			out.Delivery = "sent"
		}
		if check {
			out.Waiting = "waiting"
			var n int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM exch_notices WHERE delivery_id=?`, r.DeliveryID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				out.Waiting = "raised"
			}
		}
		if len(body) > 0 {
			var a Answer
			if json.Unmarshal(body, &a) == nil {
				out.Report = &a
			}
		}
		return nil
	})
	return out, err
}

// Report retains the receiver's claim without asserting that the sender ran its
// named check. Answer remains the separate, checked-completion operation.
func (s *Store) Report(ctx context.Context, delivery, actor string, a Answer, at time.Time) error {
	if !timestamp(at) {
		return ErrInvalid
	}
	a, err := normalizeAnswer(a)
	if err != nil {
		return err
	}
	body := string(encode(a))
	material, err := s.stageBody(ctx, body)
	if err != nil {
		return err
	}
	defer closeMaterial(material)
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := forDelivery(ctx, tx, delivery)
		if err != nil {
			return err
		}
		if r.To != actor {
			return ErrInvalid
		}
		if r.State == Answered {
			return nil
		}
		if _, err = deliver(ctx, tx, r, at); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE exch_msg SET state='answered',answer=? WHERE delivery_id=?`, body, delivery); err != nil {
			return err
		}
		if err = appendEvent(ctx, tx, r, actor, "report", body, at, material); err != nil {
			return err
		}
		return raise(ctx, tx, at)
	})
}
func raise(ctx context.Context, tx *canon.Tx, at time.Time) error {
	// A report wins when report and due date are first observed together; a
	// report arriving after an earlier due notice still gets its own notice.
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO exch_notices(id,delivery_id,reason)
 SELECT 'handoff-report:'||o.delivery_id,o.delivery_id,'report' FROM exch_origins o
 JOIN exch_msg m USING(delivery_id) WHERE o.check_back=1 AND m.state='answered'
 AND EXISTS(SELECT 1 FROM exch_event x WHERE x.delivery_id=o.delivery_id AND x.kind IN ('report','checked','acknowledged'))`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO exch_notices(id,delivery_id,reason)
 SELECT 'handoff-due:'||o.delivery_id,o.delivery_id,'next_check' FROM exch_origins o
 JOIN exch_msg m USING(delivery_id) WHERE o.check_back=1 AND o.next_check<=?
 AND NOT EXISTS(SELECT 1 FROM exch_notices n WHERE n.delivery_id=o.delivery_id AND n.reason='report')`, at.UnixNano())
	return err
}
func (s *Store) Raise(ctx context.Context, at time.Time) error {
	if !timestamp(at) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error { return raise(ctx, tx, at) })
}
func (s *Store) Notices(ctx context.Context, person string) ([]Notice, error) {
	var result []Notice
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT n.id,n.delivery_id,e.id,o.person,e.from_agent,o.conversation,n.reason FROM exch_notices n JOIN exch_origins o USING(delivery_id) JOIN exch_msg m USING(delivery_id) JOIN exchanges e ON e.id=m.exchange_id WHERE o.person=? AND n.delivered=0 ORDER BY n.rowid`, person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n Notice
			if err = rows.Scan(&n.ID, &n.DeliveryID, &n.ExchangeID, &n.Person, &n.Agent, &n.Conversation, &n.Reason); err != nil {
				return err
			}
			result = append(result, n)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Store) NoticeDelivered(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE exch_notices SET delivered=1 WHERE id=?`, id)
		return err
	})
}

// TrackedDeliveries excludes old package-only exchanges without a Core origin.
func (s *Store) TrackedDeliveries(ctx context.Context, person string) ([]Receipt, error) {
	all, err := s.PendingDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	var out []Receipt
	for _, r := range all {
		o, err := s.Origin(ctx, r.DeliveryID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if o.Person == person {
			out = append(out, r)
		}
	}
	return out, nil
}
