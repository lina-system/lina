// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Completion is a signal to re-read work, never an instruction or a verdict.
// Attempt is the Core execution ID, distinct from a child's receipt counter.
// Head is empty for results without an observed Git head.
type Completion struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Attempt   string `json:"attempt"`
	Head      string `json:"head"`
	ReceiptID string `json:"receipt_id"`
	Revision  string `json:"revision,omitempty"`
}

var gitHead = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// FinishIn composes a trusted terminal observation with its producer's state.
// No caller may react until the enclosing canon transaction has committed.
func FinishIn(ctx context.Context, tx *canon.Tx, taskID, runID, status, head string) error {
	if head != "" && !gitHead.MatchString(head) {
		return ErrInvalid
	}
	return appendEvent(ctx, tx, Event{Key: "finish:" + runID, TaskID: taskID, Kind: "finished", RunID: runID, Value: status, Head: head})
}

// ObserveHead is a Core-only observation, not worker-supplied receipt data.
// Moving the current head invalidates old signals without inventing a delivery.
func (s *Service) ObserveHead(ctx context.Context, taskID, runID, head string) error {
	if !gitHead.MatchString(head) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		events, err := ledger(ctx, tx, taskID)
		if err != nil {
			return err
		}
		c, err := Project(events)
		if err != nil {
			return err
		}
		if c.Run == nil || c.Run.ID != runID {
			return ErrState
		}
		if c.Head == head {
			return nil
		}
		return appendEvent(ctx, tx, Event{Key: "head:" + digest(fmt.Sprintf("%s:%d", runID, len(events))), TaskID: taskID, Kind: "head_observed", RunID: runID, Head: head})
	})
}

// RestoreCompletionHead upgrades a pre-outbox terminal observation using the
// dispatcher's already persisted PR evidence. It cannot replace a known head.
func (s *Service) RestoreCompletionHead(ctx context.Context, taskID, runID, head string) error {
	if !gitHead.MatchString(head) {
		return ErrInvalid
	}
	return s.append(ctx, Event{Key: "completion-head:" + runID, TaskID: taskID, Kind: "completion_head", RunID: runID, Head: head})
}

func completionFor(c Card) (Completion, bool) {
	if c.Run == nil || c.Active || c.TurnStatus == "" || c.Receipt == nil {
		return Completion{}, false
	}
	e := Completion{TaskID: c.Task.ID, Attempt: c.Run.ID, Head: c.CompletionHead, ReceiptID: c.Receipt.EventID}
	if c.FileSnapshot != nil {
		e.Revision = c.FileSnapshot.Fingerprint
	}
	key, _ := json.Marshal([]string{e.TaskID, e.Attempt, e.Head, e.ReceiptID, e.Revision})
	e.ID = digest(string(key))
	return e, true
}
func insertCompletion(ctx context.Context, tx *canon.Tx, c Card) error {
	e, ok := completionFor(c)
	if !ok {
		return nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO work_completions(id,task_id,attempt,head,record) VALUES(?,?,?,?,?) ON CONFLICT(task_id,attempt,head) DO UPDATE SET id=excluded.id,record=excluded.record WHERE work_completions.id<>excluded.id`, e.ID, e.TaskID, e.Attempt, e.Head, raw)
	return err
}

// ReconcileCompletions restores missing signals in bounded writer batches.
// Recover rebuilds the card index from the ledger before the startup sweep.
// Claims are separate tombstones: losing an event cannot replay a handled one.
func (s *Service) ReconcileCompletions(ctx context.Context) error {
	cursor := ""
	for {
		var cards []Card
		err := s.db.Write(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards WHERE task_id>? ORDER BY task_id LIMIT 128", cursor)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw []byte
				var c Card
				if err = rows.Scan(&raw); err != nil {
					break
				}
				if err = json.Unmarshal(raw, &c); err != nil {
					break
				}
				cards = append(cards, c)
			}
			err = errors.Join(err, rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			for _, c := range cards {
				if err = insertCompletion(ctx, tx, c); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(cards) < 128 {
			return nil
		}
		cursor = cards[len(cards)-1].Task.ID
	}
}

func (s *Service) PendingCompletions(ctx context.Context, epoch string) ([]Completion, error) {
	var result []Completion
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT e.record FROM work_completions e LEFT JOIN work_completion_claims c ON c.id=e.id WHERE c.id IS NULL OR (c.state='claimed' AND c.epoch<>?) ORDER BY e.task_id,e.id`, epoch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var e Completion
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &e); err != nil {
				return err
			}
			result = append(result, e)
		}
		return rows.Err()
	})
	return result, err
}

// ClaimCompletion rechecks the authoritative current projection and takes one
// epoch-bound claim. A restarted Core can resume an unacknowledged claim; the
// reaction must use e.ID for durable idempotency at its own boundary.
func (s *Service) ClaimCompletion(ctx context.Context, id, epoch string) (e Completion, c Card, claimed bool, err error) {
	if epoch == "" {
		return e, c, false, ErrInvalid
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM work_completions WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		var state, priorEpoch string
		err := tx.QueryRowContext(ctx, "SELECT state,epoch FROM work_completion_claims WHERE id=?", id).Scan(&state, &priorEpoch)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state == "handled" || state == "ignored" || state == "claimed" && priorEpoch == epoch {
			return nil
		}
		if err = tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", e.TaskID).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		current, ok := completionFor(c)
		state = "ignored"
		if ok && current == e && c.Head == e.Head {
			state = "claimed"
			claimed = true
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO work_completion_claims(id,epoch,state) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,state=excluded.state`, id, epoch, state)
		return err
	})
	if err != nil {
		claimed = false
	}
	return
}
func (s *Service) AcknowledgeCompletion(ctx context.Context, id, epoch string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE work_completion_claims SET state='handled' WHERE id=? AND epoch=? AND state='claimed'", id, epoch)
		return err
	})
}

// RetryCompletions releases only this reactor's unsuccessful claims at its
// bounded retry boundary. Successful and ignored tombstones never change.
func (s *Service) RetryCompletions(ctx context.Context, epoch string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE work_completion_claims SET epoch='' WHERE epoch=? AND state='claimed'", epoch)
		return err
	})
}
