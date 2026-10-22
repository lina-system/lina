// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrControlHeld = errors.New("task is controlled externally or its transfer is held")

// Switch contains only LINA identities and opaque evidence. Only an authenticated
// owner-facing service may admit its approval; imported text is not authority.
type Switch struct {
	ID       string        `json:"id"`
	Owner    string        `json:"owner"`
	Approval string        `json:"approval"`
	Snapshot string        `json:"snapshot"`
	Tasks    []string      `json:"tasks"`
	State    string        `json:"state"`
	Events   []SwitchEvent `json:"events"`
}
type SwitchEvent struct {
	State    string `json:"state"`
	Evidence string `json:"evidence"`
}

func readSwitch(ctx context.Context, tx *canon.Tx, id string) (v Switch, err error) {
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT record FROM work_switches WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return
}
func (s *Service) GetSwitch(ctx context.Context, id string) (v Switch, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { v, err = readSwitch(ctx, tx, id); return err })
	return
}
func saveSwitch(ctx context.Context, tx *canon.Tx, v Switch, e SwitchEvent) error {
	v.Events = append(v.Events, e)
	v.State = e.State
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO work_switches(id,record) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record", v.ID, raw); err != nil {
		return err
	}
	raw, err = json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO work_switch_events(switch_id,record) VALUES(?,?)", v.ID, raw)
	return err
}

// ApproveSwitch holds every task in the same canon transaction that records the
// explicit owner approval. It does not stop or communicate with another system.
func (s *Service) ApproveSwitch(ctx context.Context, v Switch) error {
	hash, err := hex.DecodeString(v.Snapshot)
	if err != nil || len(hash) != 32 || v.ID == "" || v.Owner == "" || strings.TrimSpace(v.Approval) == "" || len(v.Tasks) == 0 || len(v.Tasks) > 256 || v.State != "" || len(v.Events) != 0 {
		return ErrInvalid
	}
	v.Tasks = slices.Clone(v.Tasks)
	slices.Sort(v.Tasks)
	if len(slices.Compact(slices.Clone(v.Tasks))) != len(v.Tasks) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		prior, err := readSwitch(ctx, tx, v.ID)
		if err == nil {
			prior.State = ""
			prior.Events = nil
			if reflect.DeepEqual(prior, v) {
				return nil
			}
			return ErrState
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		// Exactly one procedure may be in flight, even for disjoint task sets.
		rows, err := tx.QueryContext(ctx, "SELECT record FROM work_switches")
		if err != nil {
			return err
		}
		busy := false
		for rows.Next() {
			var b []byte
			var old Switch
			if err = rows.Scan(&b); err != nil {
				break
			}
			if err = json.Unmarshal(b, &old); err != nil {
				break
			}
			if old.State != "rolled_back" {
				busy = true
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if busy {
			return ErrState
		}
		for _, id := range v.Tasks {
			var raw []byte
			if err = tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", id).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			var card Card
			if err = json.Unmarshal(raw, &card); err != nil {
				return err
			}
			if card.Task.PersonID != v.Owner || !card.Task.ExternalControl {
				return ErrInvalid
			}
			if err = workerInDirectory(ctx, tx, card.Task); err != nil {
				return err
			}
			if card.Active {
				return ErrActiveRun
			}
			var other string
			err = tx.QueryRowContext(ctx, "SELECT switch_id FROM work_switch_tasks WHERE task_id=?", id).Scan(&other)
			if err == nil {
				return ErrState
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err = saveSwitch(ctx, tx, v, SwitchEvent{State: "approved", Evidence: v.Approval}); err != nil {
			return err
		}
		for _, id := range v.Tasks {
			if _, err = tx.ExecContext(ctx, "INSERT INTO work_switch_tasks(task_id,switch_id) VALUES(?,?)", id, v.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Service) ActivateSwitch(ctx context.Context, id, owner, evidence string) error {
	return s.transitionSwitch(ctx, id, owner, evidence, "active")
}
func (s *Service) RequestRollback(ctx context.Context, id, owner, reason string) error {
	return s.transitionSwitch(ctx, id, owner, reason, "rollback_pending")
}
func (s *Service) CompleteRollback(ctx context.Context, id, owner, evidence string) error {
	return s.transitionSwitch(ctx, id, owner, evidence, "rolled_back")
}
func (s *Service) transitionSwitch(ctx context.Context, id, owner, evidence, next string) error {
	if strings.TrimSpace(evidence) == "" || len(evidence) > 4096 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		v, err := readSwitch(ctx, tx, id)
		if err != nil {
			return err
		}
		if owner != v.Owner {
			return ErrInvalid
		}
		if v.State == next {
			if v.Events[len(v.Events)-1].Evidence == evidence {
				return nil
			}
			return ErrState
		}
		switch next {
		case "active":
			if v.State != "approved" {
				return ErrState
			}
		case "rollback_pending":
			if v.State != "approved" && v.State != "active" {
				return ErrState
			}
		case "rolled_back":
			if v.State != "rollback_pending" {
				return ErrState
			}
			var active int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM work_active_runs a JOIN work_switch_tasks t ON a.task_id=t.task_id WHERE t.switch_id=?", id).Scan(&active); err != nil {
				return err
			}
			var workers int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM work_worker_reservations r JOIN work_switch_tasks t ON r.task_id=t.task_id WHERE t.switch_id=?", id).Scan(&workers); err != nil {
				return err
			}
			if active != 0 || workers != 0 {
				return ErrActiveRun
			}
		default:
			return ErrInvalid
		}
		return saveSwitch(ctx, tx, v, SwitchEvent{State: next, Evidence: evidence})
	})
}

// mayDirect is evaluated inside the same write as run admission. Unknown and
// rollback-pending runs remain reserved across restart, so rollback cannot
// release the other coordinator on a timeout or merely missing receipt.
func mayDirect(ctx context.Context, tx *canon.Tx, task string) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT s.record FROM work_switches s JOIN work_switch_tasks t ON s.id=t.switch_id WHERE t.task_id=?", task).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", task).Scan(&raw); err != nil {
			return err
		}
		var card Card
		if err = json.Unmarshal(raw, &card); err != nil {
			return err
		}
		if card.Task.ExternalControl {
			return ErrControlHeld
		}
		return nil
	}
	if err != nil {
		return err
	}
	var v Switch
	if err = json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.State != "active" {
		return ErrControlHeld
	}
	return nil
}
