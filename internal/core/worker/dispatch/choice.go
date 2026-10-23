// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type Factory struct {
	Name string
	Open func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error)
}

func AntigravityFactory(cfg Config) Factory {
	return Factory{Name: "antigravity", Open: func(ctx context.Context, l lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a := antigravity.New(antigravity.Config{Binary: cfg.Binary, Environment: cfg.Environment, Epoch: cfg.Epoch, Leaser: cfg.Activities})
		return a, a.Close, nil
	}}
}
func CodexFactory(binary string) Factory {
	return Factory{Name: "codex", Open: func(ctx context.Context, l lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a, err := codex.Open(ctx, l, codex.Config{Executable: binary})
		if err != nil {
			return nil, nil, err
		}
		return a, func(context.Context) error { return a.Close() }, nil
	}}
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// claim compares declarations in the same fenced transaction that records the
// handoff. Worker termination is not landing and cannot free an area.
func (s *Service) claim(ctx context.Context, r *Record) (bool, error) {
	slots := 1
	if s.cfg.Capacity != nil {
		slots = s.cfg.Capacity(ctx)
	}
	claimed := false
	err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM worker_dispatch ORDER BY id")
		if err != nil {
			return err
		}
		var records []Record
		for rows.Next() {
			var raw []byte
			var v Record
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				rows.Close()
				return err
			}
			records = append(records, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		found := false
		for _, v := range records {
			if v.ID == r.ID {
				if v.State != "queued" {
					return nil
				}
				*r = v
				found = true
			}
		}
		if !found {
			return work.ErrNotFound
		}
		original := *r
		r.Wait = ""
		for _, v := range records {
			if v.ID == r.ID {
				continue
			}
			if v.State == "running" || v.State == "unknown" {
				slots--
			}
			held := v.AreaHeld || v.State == "running" || v.State == "unknown" || v.State == "finished"
			if !held || (v.Definition.Repository != r.Definition.Repository && (v.RepositoryKey == "" || r.RepositoryKey == "" || v.RepositoryKey != r.RepositoryKey)) {
				continue
			}
			// Only a Core-verified application frees a delivered task's lease. Missing
			// or unreadable evidence keeps the reservation, including after restart.
			var cardRaw []byte
			readErr := tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", v.ID).Scan(&cardRaw)
			if readErr == nil {
				var card work.Card
				if json.Unmarshal(cardRaw, &card) == nil && card.Applied {
					continue
				}
			}
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return readErr
			}
			for _, a := range r.Definition.WriteArea {
				for _, b := range v.Definition.WriteArea {
					if work.AreasOverlap(a, b) {
						r.Wait = "area"
					}
				}
			}
		}
		if r.Wait == "" && len(s.cfg.Workers) > 0 {
			var candidates []work.WorkerCandidate
			for _, f := range s.cfg.Workers {
				var raw []byte
				var quota []provider.QuotaRow
				err := tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", f.Name).Scan(&raw)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err == nil {
					if err = json.Unmarshal(raw, &quota); err != nil {
						return err
					}
				}
				candidates = append(candidates, work.WorkerCandidate{Name: f.Name, Slots: 1, Quota: quota})
			}
			if work.ChooseWorker(candidates, s.cfg.Rules, []string{r.Definition.Kind, r.Definition.Repository}, r.Tried, time.Now()) == "" {
				r.Wait = "worker_quota"
			}
		}
		if r.Wait == "" && slots <= 0 {
			r.Wait = "host_capacity"
		}
		if r.Wait == "" {
			r.State = "running"
			r.AreaHeld = true
			claimed = true
		}
		if reflect.DeepEqual(original, *r) {
			return nil
		}
		_, err = tx.ExecContext(ctx, "UPDATE worker_dispatch SET state=?,record=? WHERE id=?", r.State, mustJSON(r), r.ID)
		return err
	})
	return claimed, err
}
func (s *Service) quota(ctx context.Context, name string) ([]provider.QuotaRow, error) {
	var raw []byte
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", name).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []provider.QuotaRow
	err = json.Unmarshal(raw, &rows)
	return rows, err
}
func (s *Service) ObserveQuota(ctx context.Context, name string, rows []provider.QuotaRow) error {
	rows = append([]provider.QuotaRow(nil), rows...)
	for i := range rows {
		if rows[i].Observed.IsZero() {
			rows[i].Observed = time.Now()
		}
	}
	err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		var raw []byte
		var prior []provider.QuotaRow
		err := tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", name).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = json.Unmarshal(raw, &prior); err != nil {
				return err
			}
			var blockedAt time.Time
			for _, q := range prior {
				if q.State == provider.QuotaBlocked && q.Observed.After(blockedAt) {
					blockedAt = q.Observed
				}
			}
			if !blockedAt.IsZero() {
				// A delayed endpoint reply cannot erase a newer observed stop.
				// No measurement is not evidence of recovery.
				if len(rows) == 0 {
					return nil
				}
				for _, q := range rows {
					if !q.Observed.After(blockedAt) {
						return nil
					}
				}
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO worker_quota(worker,rows) VALUES(?,?) ON CONFLICT(worker) DO UPDATE SET rows=excluded.rows", name, mustJSON(rows))
		return err
	})
	if err == nil {
		s.Wake()
	}
	return err
}

// selectWorker probes only eligible workers. A blocked cache survives restart;
// unknown/absent rows remain usable. Each selected session is owned by this
// attempt, and every unused probe is closed before handoff.
func (s *Service) selectWorker(ctx context.Context, l lease.Lease, r Record) (worker.Adapter, func(context.Context) error, error) {
	type opened struct {
		a     worker.Adapter
		close func(context.Context) error
	}
	sessions := map[string]opened{}
	eligible := false
	var candidates []work.WorkerCandidate
	defer func() {
		for _, v := range sessions {
			v.close(context.Background())
		}
	}()
	for _, f := range s.cfg.Workers {
		cached, err := s.quota(ctx, f.Name)
		if err != nil {
			return nil, nil, err
		}
		c := work.WorkerCandidate{Name: f.Name, Slots: 1, Quota: cached}
		if work.ChooseWorker([]work.WorkerCandidate{c}, nil, nil, r.Tried, time.Now()) == "" {
			continue
		}
		eligible = true
		a, close, err := f.Open(ctx, l)
		if err != nil {
			continue
		}
		sessions[f.Name] = opened{a, close}
		rows, err := a.Quota(ctx)
		if err == nil && len(rows) > 0 {
			c.Quota = rows
			if err = s.ObserveQuota(ctx, f.Name, rows); err != nil {
				return nil, nil, err
			}
		}
		candidates = append(candidates, c)
	}
	chosen := work.ChooseWorker(candidates, s.cfg.Rules, []string{r.Definition.Kind, r.Definition.Repository}, r.Tried, time.Now())
	if chosen == "" {
		if eligible && len(sessions) == 0 && len(candidates) == 0 {
			return nil, nil, errors.New("worker unavailable")
		}
		return nil, nil, nil
	}
	selected := sessions[chosen]
	delete(sessions, chosen)
	return selected.a, selected.close, nil
}
func (s *Service) park(ctx context.Context, a worker.Adapter) error {
	rows, err := a.Quota(ctx)
	if err != nil || len(rows) == 0 {
		rows, err = s.quota(ctx, a.Name())
		if err != nil {
			return err
		}
	}
	now := time.Now()
	if len(rows) == 0 {
		rows = []provider.QuotaRow{{State: provider.QuotaBlocked}}
	}
	for i := range rows {
		rows[i].State = provider.QuotaBlocked
		rows[i].Source = provider.ErrorCode
		rows[i].Observed = now
		if rows[i].ResetsAt != nil && !rows[i].ResetsAt.After(now) {
			rows[i].ResetsAt = nil
		}
	}
	return s.ObserveQuota(ctx, a.Name(), rows)
}

// Explicit reset times wake selection exactly at that boundary. The bounded
// safety sweep only refreshes host resources and missed external observations.
func (s *Service) nextWake() time.Duration {
	delay := time.Minute
	_ = s.cfg.DB.Read(s.ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(s.ctx, "SELECT rows FROM worker_quota")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var quota []provider.QuotaRow
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &quota); err != nil {
				return err
			}
			for _, q := range quota {
				if q.State == provider.QuotaBlocked && q.ResetsAt != nil {
					until := time.Until(*q.ResetsAt)
					if until > 0 && until < delay {
						delay = until
					}
				}
			}
		}
		return rows.Err()
	})
	return delay
}

var errWorkerParked = errors.New("worker quota changed before launch")

func (s *Service) reserveWorker(ctx context.Context, r *Record, name string) error {
	return s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		var raw []byte
		var quota []provider.QuotaRow
		err := tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", name).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = json.Unmarshal(raw, &quota); err != nil {
				return err
			}
		}
		if work.ChooseWorker([]work.WorkerCandidate{{Name: name, Slots: 1, Quota: quota}}, nil, nil, r.Tried, time.Now()) == "" {
			return errWorkerParked
		}
		var saved Record
		if err = tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", r.ID).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &saved); err != nil {
			return err
		}
		if saved.State != "running" || len(saved.Tried) != len(r.Tried) || len(r.Tried) >= 2 {
			return work.ErrState
		}
		next := *r
		next.Worker = name
		next.Tried = append(append([]string(nil), r.Tried...), name)
		if _, err = tx.ExecContext(ctx, "UPDATE worker_dispatch SET record=? WHERE id=?", mustJSON(next), r.ID); err != nil {
			return err
		}
		*r = next
		return nil
	})
}
