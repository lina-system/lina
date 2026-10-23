// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// SteeringMessage is the session mailbox projection. Receipts are append-only
// work-ledger facts; neither delivery nor a worker's acknowledgement accepts work.
type SteeringMessage struct {
	ID              string     `json:"id"`
	Session         string     `json:"session"`
	TaskID          string     `json:"task_id"`
	Person          string     `json:"person"`
	Agent           string     `json:"agent"`
	Conversation    string     `json:"conversation"`
	Text            string     `json:"text"`
	Tier            string     `json:"tier"`
	Mechanism       string     `json:"mechanism"`
	State           string     `json:"state"`
	Reason          string     `json:"reason,omitempty"`
	SentAt          time.Time  `json:"sent_at"`
	DeliveredAt     *time.Time `json:"delivered_at,omitempty"`
	ReadAt          *time.Time `json:"read_at,omitempty"`
	ActedAt         *time.Time `json:"acted_at,omitempty"`
	ActionLatencyMS *int64     `json:"action_latency_ms,omitempty"`
}

type SteeringReceipt struct {
	ID              string     `json:"id"`
	State           string     `json:"state"`
	Reason          string     `json:"reason,omitempty"`
	At              time.Time  `json:"at"`
	ActedAt         *time.Time `json:"acted_at,omitempty"`
	ActionLatencyMS *int64     `json:"action_latency_ms,omitempty"`
}

func readSteering(ctx context.Context, tx *canon.Tx, id string) (m SteeringMessage, err error) {
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT record FROM work_steering WHERE id=?", id).Scan(&raw); err == nil {
		err = json.Unmarshal(raw, &m)
	}
	return
}
func writeSteering(ctx context.Context, tx *canon.Tx, m SteeringMessage, at time.Time) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO work_steering(id,session_key,record) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record", m.ID, m.Session, raw); err != nil {
		return err
	}
	receipt, err := json.Marshal(SteeringReceipt{ID: m.ID, State: m.State, Reason: m.Reason, At: at, ActedAt: m.ActedAt, ActionLatencyMS: m.ActionLatencyMS})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO work_steering_receipts(message_id,record) VALUES(?,?)", m.ID, receipt)
	if err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM work_cards WHERE task_id=?", m.TaskID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	seq, err := result.LastInsertId()
	if err != nil {
		return err
	}
	return appendEvent(ctx, tx, Event{Key: fmt.Sprintf("steering:%d", seq), TaskID: m.TaskID, Kind: "steering", Steering: &m})
}
func (s *Service) StoreSteering(ctx context.Context, m SteeringMessage) (SteeringMessage, error) {
	if m.ID == "" || m.Session == "" || m.Person == "" || m.Agent == "" || m.Conversation == "" || strings.TrimSpace(m.Text) == "" || len(m.Text) > 1<<20 || m.SentAt.IsZero() || (m.Tier != "push" && m.Tier != "turn" && m.Tier != "pull") {
		return m, ErrInvalid
	}
	m.State, m.Reason = "stored", ""
	m.DeliveredAt, m.ReadAt, m.ActedAt, m.ActionLatencyMS = nil, nil, nil, nil
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		prior, err := readSteering(ctx, tx, m.ID)
		if err == nil {
			if prior.Session != m.Session || prior.TaskID != m.TaskID || prior.Person != m.Person || prior.Agent != m.Agent || prior.Conversation != m.Conversation || prior.Text != m.Text {
				return ErrState
			}
			m = prior
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return writeSteering(ctx, tx, m, m.SentAt)
	})
	return m, err
}
func (s *Service) SteeringMessages(ctx context.Context, session string) (out []SteeringMessage, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM work_steering WHERE session_key=? ORDER BY rowid", session)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var m SteeringMessage
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &m); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return
}
func (s *Service) PendingSteering(ctx context.Context, session string) ([]SteeringMessage, error) {
	messages, err := s.SteeringMessages(ctx, session)
	var out []SteeringMessage
	for _, m := range messages {
		if m.State == "stored" || m.State == "held" && m.Reason != "delivery_uncertain" {
			out = append(out, m)
		}
	}
	return out, err
}

// SteeringState commits the claim before I/O. A lost reply stays held with
// delivery_uncertain and is excluded from automatic redelivery after restart.
func (s *Service) SteeringState(ctx context.Context, session, id, state, reason string, at time.Time) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		m, err := readSteering(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.Session != session {
			return ErrState
		}
		if m.State == state && m.Reason == reason {
			return nil
		}
		if m.State == "read" || m.State == "refused" {
			return ErrState
		}
		switch state {
		case "held":
			if m.Reason == "delivery_uncertain" && reason != "delivery_uncertain" {
				return ErrState
			}
			if m.State != "stored" && m.State != "held" {
				return ErrState
			}
		case "delivered":
			if m.State != "stored" && m.State != "held" {
				return ErrState
			}
			m.DeliveredAt = &at
		case "refused":
		default:
			return ErrInvalid
		}
		m.State, m.Reason = state, reason
		return writeSteering(ctx, tx, m, at)
	})
}

// Acknowledgements are scoped by the bound worker session, not tool arguments.
// acted is a worker claim observed at Core's clock, not an acceptance verdict.
func (s *Service) AcknowledgeSteering(ctx context.Context, session string, read, acted []string, at time.Time) error {
	if len(read)+len(acted) > 256 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		ids := map[string]bool{}
		for _, id := range read {
			ids[id] = false
		}
		for _, id := range acted {
			ids[id] = true
		}
		for id, action := range ids {
			m, err := readSteering(ctx, tx, id)
			if err != nil {
				return err
			}
			if m.Session != session || (m.State != "delivered" && m.State != "read") || at.Before(m.SentAt) {
				return ErrState
			}
			changed := m.State != "read"
			if changed {
				m.State = "read"
				m.ReadAt = &at
			}
			if action && m.ActedAt == nil {
				m.ActedAt = &at
				ms := at.Sub(m.SentAt).Milliseconds()
				m.ActionLatencyMS = &ms
				changed = true
			}
			if changed {
				if err = writeSteering(ctx, tx, m, at); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
func (s *Service) SteeringReceipts(ctx context.Context, id string) (out []SteeringReceipt, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM work_steering_receipts WHERE message_id=? ORDER BY seq", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r SteeringReceipt
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return
}

// FenceRestoredSteering runs only on a private restored database. A backup may
// predate delivery, so even formerly unsent mail must not regain send authority.
func FenceRestoredSteering(ctx context.Context, tx *canon.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_steering WHERE json_extract(record,'$.state') IN ('stored','held')")
	if err != nil {
		return err
	}
	var messages []SteeringMessage
	for rows.Next() {
		var raw []byte
		var m SteeringMessage
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			rows.Close()
			return err
		}
		messages = append(messages, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, m := range messages {
		m.State, m.Reason = "held", "delivery_uncertain"
		if err = writeSteering(ctx, tx, m, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}
