// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type Service struct {
	db          *canon.Store
	area        *Area
	materials   MaterialWriter
	workspaceMu sync.Mutex
}

func New(db *canon.Store) *Service { return &Service{db: db} }

// Recover rebuilds before Core exposes work. An unobserved execution remains
// reserved and becomes unknown; restart is never permission to run it again.
func (s *Service) Recover(ctx context.Context, epoch string) error {
	if epoch == "" {
		return ErrInvalid
	}
	if err := s.Rebuild(ctx); err != nil {
		return err
	}
	var active []string
	if err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT task_id FROM work_active_runs ORDER BY task_id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			active = append(active, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range active {
		if err := s.append(ctx, Event{Key: "recovery:" + epoch + ":" + id, TaskID: id, Kind: "unknown"}); err != nil {
			return err
		}
	}
	return s.recoverFileCopies(ctx)
}
func (s *Service) Create(ctx context.Context, t Task) error {
	if t.ID == "" || t.AgentID == "" || t.ConversationID == "" || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Done) == "" {
		return ErrInvalid
	}
	if t.Destination != "" && (!absolute(t.Destination) || !t.RequiresApply || t.Workspace == nil || t.Workspace.Spec.RepositoryID != "") {
		return ErrInvalid
	}
	for _, root := range t.ArtifactRoots {
		if !absolute(root) {
			return ErrInvalid
		}
	}
	return s.append(ctx, Event{Key: "task:" + t.ID, TaskID: t.ID, Kind: "created", Task: &t})
}
func (s *Service) Start(ctx context.Context, r Run) error {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if r.ID == "" || r.TaskID == "" || r.Worker == "" || r.ThreadID == "" || r.TurnID == "" || r.NodeID == "" || r.Generation < 1 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var workers int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM work_worker_reservations WHERE task_id=?", r.TaskID).Scan(&workers); err != nil {
			return err
		}
		if workers != 0 {
			return ErrActiveRun
		}
		if err := mayDirect(ctx, tx, r.TaskID); err != nil {
			return err
		}
		return appendEvent(ctx, tx, Event{Key: "run:" + r.ID, TaskID: r.TaskID, Kind: "started", Run: &r})
	})
}
func (s *Service) Submit(ctx context.Context, runID string, r Receipt) error {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	c, err := s.Get(ctx, r.RelationshipID)
	if err != nil {
		return err
	}
	if c.Task.Destination != "" && r.Outcome == "ready_for_review" {
		return s.snapshotFiles(ctx, c, runID, r)
	}
	return s.append(ctx, Event{Key: "receipt:" + r.EventID, TaskID: r.RelationshipID, Kind: "receipt", RunID: runID, Receipt: &r})
}

// Finish records a trusted worker termination observation. Model and worker
// receipt handlers must not call it using a receipt's claimed turn status.
func (s *Service) Finish(ctx context.Context, taskID, runID, status string) error {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	return s.append(ctx, Event{Key: "finish:" + runID, TaskID: taskID, Kind: "finished", RunID: runID, Value: status})
}

// Judge and Apply are Core-only APIs. The worker-facing transport must never
// install handlers for them or accept an arbitrary Event.
func (s *Service) Judge(ctx context.Context, taskID, receiptID, verdict, evidence string) error {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	return s.append(ctx, Event{Key: "judge:" + receiptID, TaskID: taskID, Kind: "judged", ReceiptID: receiptID, Value: verdict, Evidence: evidence})
}
func (s *Service) Apply(ctx context.Context, taskID, receiptID, evidence string) error {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	return s.append(ctx, Event{Key: "apply:" + receiptID, TaskID: taskID, Kind: "applied", ReceiptID: receiptID, Evidence: evidence})
}

func ledger(ctx context.Context, tx *canon.Tx, id string) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_ledger WHERE task_id=? ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var b []byte
		var e Event
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (s *Service) Ledger(ctx context.Context, id string) (events []Event, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { events, err = ledger(ctx, tx, id); return err })
	return
}
func (s *Service) append(ctx context.Context, e Event) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error { return appendEvent(ctx, tx, e) })
}
func appendEvent(ctx context.Context, tx *canon.Tx, e Event) error {
	// Compare the durable representation, excluding Go-only clock/location
	// metadata and caller-owned pointers, on both initial admission and retry.
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &e); err != nil {
		return err
	}
	events, err := ledger(ctx, tx, e.TaskID)
	if err != nil {
		return err
	}
	var c Card
	if len(events) > 0 {
		c, err = Project(events)
		if err != nil {
			return err
		}
	}
	// Do not let retries from an old generation overwrite or acknowledge the
	// current worker's delivery. Identical current-generation retries are safe.
	if e.Kind == "receipt" && (c.Run == nil || c.Run.ID != e.RunID) {
		return ErrState
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT record FROM work_ledger WHERE event_key=?", e.Key).Scan(&prior)
	if err == nil {
		var old Event
		if err = json.Unmarshal(prior, &old); err != nil {
			return err
		}
		if reflect.DeepEqual(old, e) {
			return nil
		}
		return ErrState
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = advance(&c, e); err != nil {
		return err
	}
	if e.Kind == "created" && c.Task.ExternalControl {
		// An existing untracked worker has no task gate to recheck. Refuse
		// adoption in the same transaction as its directory reservation;
		// otherwise that worker could keep steering externally owned work.
		if err = workerInDirectory(ctx, tx, c.Task); err != nil {
			return err
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", e.TaskID, e.Key, b); err != nil {
		return err
	}
	if err = saveCard(ctx, tx, c); err != nil {
		return err
	}
	if e.Kind == "finished" || e.Kind == "receipt" || e.Kind == "file_receipt" || e.Kind == "completion_head" {
		return insertCompletion(ctx, tx, c)
	}
	return nil
}
func saveCard(ctx context.Context, tx *canon.Tx, c Card) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO work_cards(task_id,record) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET record=excluded.record", c.Task.ID, b); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM work_active_runs WHERE task_id=?", c.Task.ID); err != nil {
		return err
	}
	if c.Active {
		_, err = tx.ExecContext(ctx, "INSERT INTO work_active_runs(task_id,run_id) VALUES(?,?)", c.Task.ID, c.Run.ID)
	}
	return err
}
func (s *Service) Get(ctx context.Context, id string) (c Card, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		var b []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", id).Scan(&b)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &c)
	})
	return
}

// Rebuild atomically replaces every derived card and active-run index. It can
// run with writers active: canon serializes both through its fenced writer.
func (s *Service) Rebuild(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT task_id,record FROM work_ledger ORDER BY seq")
		if err != nil {
			return err
		}
		cards := map[string]Card{}
		for rows.Next() {
			var id string
			var b []byte
			var e Event
			if err = rows.Scan(&id, &b); err != nil {
				break
			}
			if err = json.Unmarshal(b, &e); err != nil {
				break
			}
			c := cards[id]
			if err = advance(&c, e); err != nil {
				break
			}
			cards[id] = c
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM work_cards; DELETE FROM work_active_runs"); err != nil {
			return err
		}
		for _, c := range cards {
			if err = saveCard(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// Revision pins tool authority to the current projected facts. No task is
// revision zero; successful writes return the new revision to the effect store.
func (s *Service) Revision(ctx context.Context, id string) (string, error) {
	c, err := s.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return "0", nil
	}
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return digest(string(raw)), nil
}
