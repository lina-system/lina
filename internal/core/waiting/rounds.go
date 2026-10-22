// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const DoNotAct = "do_not_act"

var ErrRoundOpen = errors.New("goal already has an open question round")

// Round is one interruption containing every independent question currently
// known for a goal. Only the work owner admits it, before its dependents start.
type Round struct {
	ID, Goal, Person, Agent, Conversation string
	Deadline                              time.Time
	Questions                             []Question
}
type Question struct {
	ID, Prompt, Recommendation string
	Reason                     Reason
	Irreversible               bool
	Choices                    []string
	Default                    string
	Dependents                 []string
}
type RoundAnswer struct {
	MessageID, Message string
	Values             map[string]string
}
type RoundDecision struct {
	RoundAnswer
	Kind string
	At   time.Time
}
type RoundRecord struct {
	Round
	Decisions []RoundDecision
}

// Hooks commit work-ledger transitions in the SAME transaction. They must not
// perform external I/O. An answer is a decision, never an authority grant.
type RoundHooks struct {
	Opened  func(context.Context, *canon.Tx, Round) error
	Decided func(context.Context, *canon.Tx, Round, RoundDecision) error
}
type Rounds struct {
	db    Canon
	hooks RoundHooks
	wake  chan struct{}
}

func NewRounds(db Canon, hooks RoundHooks) *Rounds {
	return &Rounds{db: db, hooks: hooks, wake: make(chan struct{}, 1)}
}
func (s *Rounds) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func validRound(r Round) bool {
	if r.ID == "" || r.Goal == "" || r.Person == "" || r.Agent == "" || r.Conversation == "" || r.Deadline.IsZero() || r.Deadline.UnixNano() <= 0 || len(r.Questions) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, q := range r.Questions {
		if q.ID == "" || seen[q.ID] || strings.TrimSpace(q.Prompt) == "" || strings.TrimSpace(q.Recommendation) == "" || q.Default == "" || !slices.Contains(q.Choices, q.Default) || len(q.Dependents) == 0 {
			return false
		}
		seen[q.ID] = true
		switch q.Reason {
		case OutsideEffect, SignificantChoice, MissingInformation, PersonalAction:
		default:
			return false
		}
		if (q.Reason == OutsideEffect || q.Irreversible) && q.Default != DoNotAct {
			return false
		}
		choices := map[string]bool{}
		for _, v := range q.Choices {
			if strings.TrimSpace(v) == "" || choices[v] {
				return false
			}
			choices[v] = true
		}
		for _, id := range q.Dependents {
			if id == "" || id == r.Goal {
				return false
			}
		}
	}
	return true
}
func (s *Rounds) Open(ctx context.Context, r Round) error {
	if !validRound(r) {
		return ErrInvalid
	}
	r.Deadline = r.Deadline.UTC()
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var prior []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM work_waiting_rounds WHERE id=?", r.ID).Scan(&prior)
		if err == nil {
			var old Round
			if err = json.Unmarshal(prior, &old); err != nil {
				return err
			}
			if reflect.DeepEqual(old, r) {
				return nil
			}
			return ErrStale
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM work_waiting_rounds r WHERE goal=? AND NOT EXISTS (SELECT 1 FROM work_waiting_decisions d WHERE d.round_id=r.id)`, r.Goal).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrRoundOpen
		}
		if s.hooks.Opened != nil {
			if err = s.hooks.Opened(ctx, tx, r); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO work_waiting_rounds(id,goal,person,deadline,record) VALUES(?,?,?,?,?)", r.ID, r.Goal, r.Person, r.Deadline.UnixNano(), raw)
		return err
	})
	if err == nil {
		s.signal()
	}
	return err
}
func readRound(ctx context.Context, tx *canon.Tx, id string) (r RoundRecord, err error) {
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT record FROM work_waiting_rounds WHERE id=?", id).Scan(&raw); err != nil {
		return
	}
	if err = json.Unmarshal(raw, &r.Round); err != nil {
		return
	}
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_waiting_decisions WHERE round_id=? ORDER BY seq", id)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d RoundDecision
		if err = rows.Scan(&raw); err != nil {
			return
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return
		}
		r.Decisions = append(r.Decisions, d)
	}
	err = rows.Err()
	return
}
func (s *Rounds) Get(ctx context.Context, id string) (r RoundRecord, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { r, err = readRound(ctx, tx, id); return err })
	return
}
func (s *Rounds) List(ctx context.Context, person string) (records []RoundRecord, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		ids, err := roundIDs(ctx, tx, "SELECT id FROM work_waiting_rounds WHERE person=? ORDER BY deadline,id", person)
		if err != nil {
			return err
		}
		for _, id := range ids {
			r, err := readRound(ctx, tx, id)
			if err != nil {
				return err
			}
			records = append(records, r)
		}
		return nil
	})
	return
}
func roundIDs(ctx context.Context, tx *canon.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Rounds) record(ctx context.Context, tx *canon.Tx, r Round, d RoundDecision) error {
	if s.hooks.Decided != nil {
		if err := s.hooks.Decided(ctx, tx, r, d); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO work_waiting_decisions(round_id,kind,message_id,record) VALUES(?,?,?,?)", r.ID, d.Kind, d.MessageID, raw)
	return err
}
func (s *Rounds) expire(ctx context.Context, tx *canon.Tx, r *RoundRecord, at time.Time) error {
	if len(r.Decisions) > 0 || at.Before(r.Deadline) {
		return nil
	}
	d := RoundDecision{Kind: "default", At: r.Deadline, RoundAnswer: RoundAnswer{Values: map[string]string{}}}
	for _, q := range r.Questions {
		d.Values[q.ID] = q.Default
	}
	if err := s.record(ctx, tx, r.Round, d); err != nil {
		return err
	}
	r.Decisions = append(r.Decisions, d)
	return nil
}
func (s *Rounds) Expire(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		ids, err := roundIDs(ctx, tx, `SELECT id FROM work_waiting_rounds r WHERE deadline<=? AND NOT EXISTS (SELECT 1 FROM work_waiting_decisions d WHERE d.round_id=r.id) ORDER BY deadline,id`, at.UnixNano())
		if err != nil {
			return err
		}
		for _, id := range ids {
			r, err := readRound(ctx, tx, id)
			if err != nil {
				return err
			}
			if err = s.expire(ctx, tx, &r, at); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Rounds) Answer(ctx context.Context, person, id string, a RoundAnswer, at time.Time) error {
	if person == "" || strings.TrimSpace(a.MessageID) == "" || strings.TrimSpace(a.Message) == "" || at.IsZero() {
		return ErrInvalid
	}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := readRound(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Person != person {
			return ErrPerson
		}
		if len(a.Values) != len(r.Questions) {
			return ErrInvalid
		}
		for _, q := range r.Questions {
			if !slices.Contains(q.Choices, a.Values[q.ID]) {
				return ErrInvalid
			}
		}
		for _, d := range r.Decisions {
			if d.MessageID == a.MessageID {
				if reflect.DeepEqual(d.RoundAnswer, a) {
					return nil
				}
				return ErrStale
			}
		}
		if err = s.expire(ctx, tx, &r, at); err != nil {
			return err
		}
		kind := "answer"
		if len(r.Decisions) > 0 {
			if r.Decisions[0].Kind != "default" {
				return ErrAnswered
			}
			kind = "late_answer"
		}
		return s.record(ctx, tx, r.Round, RoundDecision{RoundAnswer: a, Kind: kind, At: at.UTC()})
	})
	if err == nil {
		s.signal()
	}
	return err
}

// Run uses the nearest durable deadline, not a polling interval. Startup expiry
// is also called synchronously by Core before admitting clients. Cancellation
// stops the owned timer. A transaction/publication failure is retried, not lost.
func (s *Rounds) Run(ctx context.Context, publish func(context.Context) error) {
	for ctx.Err() == nil {
		err := s.Expire(ctx, time.Now())
		if err == nil && publish != nil {
			err = publish(ctx)
		}
		delay := time.Duration(1<<63 - 1)
		if err == nil {
			err = s.db.Read(ctx, func(tx *canon.Tx) error {
				var deadline sql.NullInt64
				if err := tx.QueryRowContext(ctx, `SELECT min(deadline) FROM work_waiting_rounds r WHERE NOT EXISTS (SELECT 1 FROM work_waiting_decisions d WHERE d.round_id=r.id)`).Scan(&deadline); err != nil {
					return err
				}
				if deadline.Valid {
					delay = max(0, time.Until(time.Unix(0, deadline.Int64)))
				}
				return nil
			})
		}
		if err != nil {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
