// SPDX-License-Identifier: Apache-2.0

// Package jobs owns the Moirai job API. The worker never receives a writer,
// credential, provider adapter or unvalidated route to a canon mutation.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

// Schema is installed by Core's schema owner in a numbered canon migration,
// together with budget.Schema. Neither the API nor the worker migrates canon.
const Schema = canon.MoiraiJobsSchema

type Call struct {
	JobID     string
	Input     json.RawMessage
	Kind      provider.RunKind
	MaxTokens int64
}
type Result struct {
	Output json.RawMessage
	Usage  provider.Usage
}

// Definition is registered by Core, never deserialized from worker input. Model
// runs on Core's model path without tools or persona; deterministic jobs need
// no Model or account binding. Apply enforces the job's
// write rules inside the same transaction as its receipt and watermark.
type Definition struct {
	// MaxPayloadBytes bounds both queued input and proposals. Zero keeps the
	// ordinary 256 KiB cap; Core can grant bounded headroom for encoded text.
	MaxPayloadBytes int
	Agent           string
	Account         provider.AccountID
	Provider        provider.ProviderID
	Model           func(context.Context, Call) (Result, error)
	Apply           func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error
}
type Config struct {
	Store       *canon.Store
	Epoch       uint64
	Limit       int64
	Definitions map[string]Definition
	Now         func() time.Time
	// AfterApply delivers committed outbox records. A delivery error never
	// rolls back the receipt; replay can retry delivery without another write.
	AfterApply func(context.Context) error
}
type Service struct{ cfg Config }

const maxPayloadBytes = 6 << 20

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Epoch == 0 || cfg.Limit < 0 {
		return nil, jobapi.ErrInvalid
	}
	defs := make(map[string]Definition, len(cfg.Definitions))
	for id, d := range cfg.Definitions {
		if id == "" || d.Agent == "" || d.Apply == nil || d.MaxPayloadBytes < 0 || d.MaxPayloadBytes > maxPayloadBytes || (d.Model != nil && (d.Account == "" || d.Provider == "")) {
			return nil, jobapi.ErrInvalid
		}
		defs[id] = d
	}
	cfg.Definitions = defs
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

// apply is the single fenced durable mutation boundary, also used for Core's
// budget accounting and job administration. A worker can only submit typed
// proposals and reports; it cannot supply SQL or choose the write callback.
func (s *Service) apply(ctx context.Context, epoch uint64, fn func(*canon.Tx) error) error {
	if epoch != s.cfg.Epoch {
		return jobapi.ErrEpoch
	}
	err := s.cfg.Store.Write(ctx, func(tx *canon.Tx) error {
		var actual string
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&actual); err != nil {
			return err
		}
		if actual != fmt.Sprint(epoch) {
			return jobapi.ErrEpoch
		}
		return fn(tx)
	})
	if errors.Is(err, canon.ErrStaleEpoch) {
		return jobapi.ErrEpoch
	}
	return err
}
func readJob(ctx context.Context, tx *canon.Tx, id string) (jobapi.Job, error) {
	var data []byte
	var j jobapi.Job
	err := tx.QueryRowContext(ctx, "SELECT record FROM moirai_jobs WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return j, jobapi.ErrUnavailable
	}
	if err == nil {
		err = json.Unmarshal(data, &j)
	}
	return j, err
}
func saveJob(ctx context.Context, tx *canon.Tx, j jobapi.Job) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE moirai_jobs SET record=? WHERE id=?", data, j.ID)
	return err
}
func (s *Service) Register(ctx context.Context, j jobapi.Job) error {
	return s.register(ctx, j, false)
}

// Ensure registers installed policy once and retains durable progress and user
// enablement on later boots.
func (s *Service) Ensure(ctx context.Context, j jobapi.Job) error {
	return s.register(ctx, j, true)
}

func (s *Service) register(ctx context.Context, j jobapi.Job, existing bool) error {
	d, ok := s.cfg.Definitions[j.ID]
	if !ok || !j.Kind.Valid() || j.Every <= 0 || j.Timeout <= 0 || j.Timeout > 2*time.Minute || j.Budget < 0 || j.MaxTokens < 0 || j.MaxTokens > j.Budget || j.Adoption == "" || j.Watermark != 0 {
		return jobapi.ErrInvalid
	}
	if !j.Deterministic && (j.MaxTokens == 0 || d.Model == nil) {
		return jobapi.ErrInvalid
	}
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		query := "INSERT INTO moirai_jobs VALUES (?,?)"
		if existing {
			query += " ON CONFLICT(id) DO NOTHING"
		}
		_, err := tx.ExecContext(ctx, query, j.ID, data)
		return err
	})
}
func (s *Service) SetEnabled(ctx context.Context, id string, enabled bool) error {
	return s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, id)
		if err != nil {
			return err
		}
		j.Enabled = enabled
		return saveJob(ctx, tx, j)
	})
}
func (s *Service) Enqueue(ctx context.Context, id string, task jobapi.Task) error {
	d, ok := s.cfg.Definitions[id]
	if !ok || task.Sequence <= 0 || !d.validPayload(task.Input) {
		return jobapi.ErrInvalid
	}
	return s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		if _, err := readJob(ctx, tx, id); err != nil {
			return err
		}
		var last int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM moirai_tasks WHERE job=?", id).Scan(&last); err != nil {
			return err
		}
		if last == math.MaxInt64 || task.Sequence != last+1 {
			return jobapi.ErrConflict
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO moirai_tasks VALUES (?,?,?)", id, task.Sequence, []byte(task.Input))
		return err
	})
}
func validOutput(data json.RawMessage) bool {
	return (Definition{}).validPayload(data)
}
func (d Definition) validPayload(data json.RawMessage) bool {
	limit := d.MaxPayloadBytes
	if limit == 0 {
		limit = 256 << 10
	}
	return len(data) > 0 && len(data) <= limit && json.Valid(data) && string(data) != "null"
}
func (s *Service) check(ctx context.Context, tx *canon.Tx, id string, from, through int64) (jobapi.Job, jobapi.Task, error) {
	var task jobapi.Task
	j, err := readJob(ctx, tx, id)
	if err != nil {
		return j, task, err
	}
	if !j.Enabled {
		return j, task, jobapi.ErrDisabled
	}
	if from < 0 || from == math.MaxInt64 || through != from+1 || j.Watermark != from {
		return j, task, jobapi.ErrConflict
	}
	if s.cfg.Now().Before(j.NextRun) {
		return j, task, jobapi.ErrNotDue
	}
	task.Sequence = through
	var input []byte
	err = tx.QueryRowContext(ctx, "SELECT input FROM moirai_tasks WHERE job=? AND sequence=?", id, through).Scan(&input)
	task.Input = input
	if errors.Is(err, sql.ErrNoRows) {
		err = jobapi.ErrUnavailable
	}
	return j, task, err
}
func (s *Service) Aux(ctx context.Context, r jobapi.AuxRequest) (jobapi.AuxResult, error) {
	d, ok := s.cfg.Definitions[r.JobID]
	if !ok {
		return jobapi.AuxResult{}, jobapi.ErrUnavailable
	}
	var j jobapi.Job
	var task jobapi.Task
	var output []byte
	var reservation string
	cached := false
	err := s.apply(ctx, r.Epoch, func(tx *canon.Tx) error {
		var err error
		j, task, err = s.check(ctx, tx, r.JobID, r.From, r.Through)
		if err != nil {
			return err
		}
		if j.Deterministic || d.Model == nil {
			return jobapi.ErrInvalid
		}
		err = tx.QueryRowContext(ctx, "SELECT reservation,output FROM moirai_aux WHERE job=? AND sequence=?", r.JobID, r.Through).Scan(&reservation, &output)
		if err == nil {
			if output == nil {
				return jobapi.ErrPending
			}
			cached = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var used int64
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(charged),0) FROM budget_usage WHERE run=? AND kind=?", r.JobID, provider.Auxiliary).Scan(&used); err != nil {
			return err
		}
		if used >= j.Budget || j.MaxTokens > j.Budget-used {
			return jobapi.ErrBudget
		}
		reservation = rand.Text()
		err = budget.Reserve(ctx, tx, budget.Reservation{ID: reservation, Agent: d.Agent, Account: d.Account, Provider: d.Provider, Kind: provider.Auxiliary, Run: r.JobID, Tokens: j.MaxTokens}, s.cfg.Limit)
		if errors.Is(err, budget.ErrLimit) {
			return jobapi.ErrBudget
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO moirai_aux(job,sequence,reservation) VALUES (?,?,?)", r.JobID, r.Through, reservation)
		return err
	})
	if err != nil || cached {
		return jobapi.AuxResult{Output: output}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()
	result, callErr := d.Model(callCtx, Call{JobID: r.JobID, Input: task.Input, Kind: provider.Auxiliary, MaxTokens: j.MaxTokens})
	if callErr == nil {
		callErr = callCtx.Err()
	}
	if callErr != nil {
		return jobapi.AuxResult{}, jobapi.ErrUnavailable
	}
	if !validOutput(result.Output) || !result.Usage.Valid() {
		return jobapi.AuxResult{}, jobapi.ErrInvalid
	}
	err = s.apply(ctx, r.Epoch, func(tx *canon.Tx) error {
		if err := budget.Settle(ctx, tx, reservation, result.Usage); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE moirai_aux SET output=? WHERE job=? AND sequence=?", []byte(result.Output), r.JobID, r.Through)
		return err
	})
	if err != nil {
		return jobapi.AuxResult{}, err
	}
	return jobapi.AuxResult{Output: result.Output}, nil
}
func (s *Service) Apply(ctx context.Context, p jobapi.Proposal) (jobapi.Applied, error) {
	d, ok := s.cfg.Definitions[p.JobID]
	if !ok {
		return jobapi.Applied{}, jobapi.ErrUnavailable
	}
	if !d.validPayload(p.Output) || p.From < 0 || p.From == math.MaxInt64 || p.Through != p.From+1 {
		return jobapi.Applied{}, jobapi.ErrInvalid
	}
	err := s.apply(ctx, p.Epoch, func(tx *canon.Tx) error { return s.applyProposal(ctx, tx, d, p) })
	if err != nil {
		return jobapi.Applied{}, err
	}
	if s.cfg.AfterApply != nil {
		if err := s.cfg.AfterApply(ctx); err != nil {
			return jobapi.Applied{}, err
		}
	}
	return jobapi.Applied{Watermark: p.Through}, nil
}

func (s *Service) applyProposal(ctx context.Context, tx *canon.Tx, d Definition, p jobapi.Proposal) error {
	// Compact JSON whitespace consistently for worker and in-process ingress.
	encoded, _ := json.Marshal(p)
	var previous []byte
	err := tx.QueryRowContext(ctx, "SELECT proposal FROM moirai_applied WHERE job=? AND sequence=?", p.JobID, p.Through).Scan(&previous)
	if err == nil {
		if string(previous) != string(encoded) {
			return jobapi.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	j, task, err := s.check(ctx, tx, p.JobID, p.From, p.Through)
	if err != nil {
		return err
	}
	if err = d.Apply(ctx, tx, task, p.Output); err != nil {
		return err
	}
	j.Watermark = p.Through
	j.NextRun = s.cfg.Now().Add(j.Every)
	if err = saveJob(ctx, tx, j); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO moirai_applied VALUES (?,?,?)", p.JobID, p.Through, encoded)
	return err
}
func (s *Service) Report(ctx context.Context, id string, state jobapi.JobState) error {
	if state.Status != "applied" && state.Status != "failed" {
		return jobapi.ErrInvalid
	}
	return s.apply(ctx, state.Epoch, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if j.Watermark != state.Watermark {
			return jobapi.ErrConflict
		}
		data, _ := json.Marshal(state)
		_, err = tx.ExecContext(ctx, "INSERT INTO moirai_reports VALUES (?,?) ON CONFLICT(job) DO UPDATE SET record=excluded.record", id, data)
		return err
	})
}
