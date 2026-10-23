// SPDX-License-Identifier: Apache-2.0
// Recovery retains worker decisions separately from the effects they observe.
package reactor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
)

type Recovery struct{ DB *canon.Store }

type Record struct {
	PendingRequests                                         map[string]bool
	Worker                                                  string
	ID, Person, Agent, Conversation, Input, Workdir, Packet string
	Thread                                                  worker.Thread
	Epoch                                                   uint64
	State                                                   string
	Retries                                                 int
	RetryFrom                                               string
	Canceled                                                bool
	DecisionAt                                              int64
	ObservedAt                                              int64
	Decision                                                string
	DecidedAt                                               int64
}

func (s Recovery) Put(ctx context.Context, r Record) error   { return s.save(ctx, r, true) }
func (s Recovery) Admit(ctx context.Context, r Record) error { return s.save(ctx, r, false) }
func (s Recovery) save(ctx context.Context, r Record, replace bool) error {
	if r.ID == "" || r.Thread.ID != r.ID || r.Person == "" || r.Agent == "" || r.Input == "" {
		return errors.New("invalid recovery binding")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.DB.Write(ctx, func(tx *canon.Tx) error {
		query := `INSERT INTO worker_recovery(id,record) VALUES(?,?) ON CONFLICT(id) DO NOTHING`
		if replace {
			query = `INSERT INTO worker_recovery(id,record) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record`
		}
		_, err := tx.ExecContext(ctx, query, r.ID, raw)
		return err
	})
}
func (s Recovery) Records(ctx context.Context) ([]Record, error) {
	var result []Record
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT record FROM worker_recovery ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r Record
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return err
			}
			result = append(result, r)
		}
		return rows.Err()
	})
	return result, err
}

// Observe is a pure policy: callers persist the returned record before sending
// input. A retry intent with no observed new turn can never authorize a resend.
// The exact thread snapshot and every effect must be settled independently.
func Observe(r Record, snap worker.ThreadSnapshot) (Record, bool) {
	if r.State == "decision" || r.State == "finished" || r.State == "parked" || snap.Thread != r.Thread {
		return r, false
	}
	if snap.Finish == nil || !snap.EffectsKnown || len(snap.Pending) > 0 || len(r.PendingRequests) > 0 || r.RetryFrom == r.Thread.TurnID {
		if r.State != "running" || snap.State != worker.Active && snap.State != worker.Waiting {
			r.State = "unknown"
		}
		return r, false
	}
	if snap.Finish.Status == worker.Done || r.Canceled {
		r.State = "finished"
		return r, false
	}
	if snap.Finish.Failure == worker.QuotaFailure || snap.Finish.Failure == worker.RateLimitFailure {
		r.State = "parked"
		return r, false
	}
	if r.Retries >= 1 {
		r.State = "decision"
		return r, false
	}
	r.Retries++
	r.RetryFrom = r.Thread.TurnID
	r.State = "retrying"
	return r, true
}
