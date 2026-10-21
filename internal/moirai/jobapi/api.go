// SPDX-License-Identifier: Apache-2.0

// Package jobapi is the credential-free contract between Moirai and Core.
package jobapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid_job_request")
	ErrEpoch       = errors.New("stale_epoch")
	ErrConflict    = errors.New("job_conflict")
	ErrDisabled    = errors.New("job_disabled")
	ErrBudget      = errors.New("budget_reached")
	ErrUnavailable = errors.New("job_unavailable")
	ErrPending     = errors.New("aux_outcome_unknown")
	ErrNotDue      = errors.New("job_not_due")
)

type Kind string

const (
	Memory   Kind = "memory"
	Planning Kind = "planning"
)

func (k Kind) Valid() bool { return k == Memory || k == Planning }

// Job is durable Core-owned policy and progress. No account or credential is
// exposed to the worker. Budget is a token allowance until Core reconfigures it.
type Job struct {
	ID      string
	Kind    Kind
	Enabled bool
	// Deterministic is Core-owned policy: Task.Input is the proposal, with no Aux call.
	// Its zero value preserves the model route for existing jobs.
	Deterministic     bool
	Every, Timeout    time.Duration
	Budget, MaxTokens int64
	Adoption          string
	Watermark         int64
	NextRun           time.Time
}
type Task struct {
	Sequence int64
	Input    json.RawMessage
}
type Work struct {
	Epoch uint64
	Job   Job
	Task  Task
}
type AuxRequest struct {
	Epoch         uint64
	JobID         string
	From, Through int64
}
type AuxResult struct{ Output json.RawMessage }
type Proposal struct {
	Epoch         uint64
	JobID         string
	From, Through int64
	Output        json.RawMessage
}
type Applied struct{ Watermark int64 }
type JobState struct {
	Epoch     uint64
	Watermark int64
	Status    string
}

type API interface {
	Aux(context.Context, AuxRequest) (AuxResult, error)
	Apply(context.Context, Proposal) (Applied, error)
	Report(context.Context, string, JobState) error
}
