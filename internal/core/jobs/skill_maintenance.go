// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

const SkillMaintenanceJob = "skill-maintenance"

type SkillMaintenanceResult struct {
	Actions []skills.Maintenance `json:"actions"`
}

// SkillMaintenanceDefinition permits only the Core-computed output over the
// queued snapshot. An obsolete or undone action is consumed without a notice.
func SkillMaintenanceDefinition(store *skills.Store, model func(context.Context, Call) (Result, error)) Definition {
	return Definition{Model: model, Apply: func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		var cached []byte
		if err := tx.QueryRowContext(ctx, "SELECT output FROM moirai_aux WHERE job=? AND sequence=?", SkillMaintenanceJob, task.Sequence).Scan(&cached); err != nil {
			return jobapi.ErrInvalid
		}
		var a, b bytes.Buffer
		if json.Compact(&a, cached) != nil || json.Compact(&b, output) != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
			return jobapi.ErrInvalid
		}
		var snapshot []skills.Body
		if json.Unmarshal(task.Input, &snapshot) != nil {
			return jobapi.ErrInvalid
		}
		allowed := map[string]int64{}
		for _, body := range snapshot {
			allowed[body.Version.ID] = body.Version.Revision
		}
		var result SkillMaintenanceResult
		d := json.NewDecoder(bytes.NewReader(output))
		d.DisallowUnknownFields()
		if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || len(result.Actions) > 32 {
			return jobapi.ErrInvalid
		}
		used := map[string]bool{}
		for _, action := range result.Actions {
			for _, m := range append([]skills.Undo{action.Target}, action.Sources...) {
				if used[m.ID] || m.Revision <= 0 || allowed[m.ID] != m.Revision {
					return jobapi.ErrInvalid
				}
				used[m.ID] = true
			}
		}
		for _, action := range result.Actions {
			_, err := store.MaintainTx(ctx, tx, action)
			switch {
			case errors.Is(err, skills.ErrUndone), errors.Is(err, skills.ErrConflict):
				continue
			case errors.Is(err, skills.ErrInvalid), errors.Is(err, skills.ErrAcceptance):
				return jobapi.ErrInvalid
			case err != nil:
				return err
			}
		}
		return nil
	}}
}

// ScheduleSnapshot retains at most one pending pass and does not rescan an
// unchanged catalog. Neither restart nor foreground reads renew its schedule.
func (s *Service) ScheduleSnapshot(ctx context.Context, id string, input json.RawMessage) error {
	d, ok := s.cfg.Definitions[id]
	if !ok || !d.validPayload(input) {
		return jobapi.ErrInvalid
	}
	return s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if !j.Enabled {
			return nil
		}
		var seq int64
		var previous []byte
		err = tx.QueryRowContext(ctx, "SELECT sequence,input FROM moirai_tasks WHERE job=? ORDER BY sequence DESC LIMIT 1", id).Scan(&seq, &previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if seq > j.Watermark {
			_, err := tx.ExecContext(ctx, "UPDATE moirai_tasks SET input=? WHERE job=? AND sequence=? AND NOT EXISTS (SELECT 1 FROM moirai_aux WHERE job=? AND sequence=?)", []byte(input), id, seq, id, seq)
			return err
		}
		if bytes.Equal(previous, input) {
			return nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO moirai_tasks VALUES(?,?,?)", id, seq+1, []byte(input))
		return err
	})
}

func (s *Service) Policy(ctx context.Context, id string) (jobapi.Job, error) {
	var j jobapi.Job
	err := s.cfg.Store.Read(ctx, func(tx *canon.Tx) error { var err error; j, err = readJob(ctx, tx, id); return err })
	return j, err
}
