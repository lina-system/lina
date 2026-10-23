// SPDX-License-Identifier: Apache-2.0

// Package moirai executes optional background jobs using only read-only canon
// snapshots and Core's credential-free job API.
package moirai

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/internal/moirai/planning"
)

type Reader interface {
	Ready(context.Context, time.Time) ([]jobapi.Work, error)
}
type Worker struct {
	Reader Reader
	Core   jobapi.API
}

func (w Worker) Step(ctx context.Context, work jobapi.Work) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, work.Job.Timeout+5*time.Second)
	defer cancel()
	output := work.Task.Input
	if !work.Job.Deterministic {
		r, err := w.Core.Aux(ctx, jobapi.AuxRequest{Epoch: work.Epoch, JobID: work.Job.ID, From: work.Job.Watermark, Through: work.Task.Sequence})
		if err != nil {
			return err
		}
		output = r.Output
	}
	if work.Job.Kind == jobapi.Planning && work.Job.Adoption == "plan-draft-v1" && !jobapi.IsPlanRejection(output) {
		var input jobapi.PlanInput
		if json.Unmarshal(work.Task.Input, &input) != nil {
			return jobapi.ErrInvalid
		}
		draft, err := jobapi.DecodePlan(output, input.Kind)
		if err != nil {
			return err
		}
		if err = planning.Validate(input.Kind, draft); err != nil {
			return err
		}
	}
	// Core commits completion status with the receipt. A second request could
	// lose the report if this deadline expires after Apply has committed.
	_, err := w.Core.Apply(ctx, jobapi.Proposal{Epoch: work.Epoch, JobID: work.Job.ID, From: work.Job.Watermark, Through: work.Task.Sequence, Output: output})
	return err
}

// Once is useful for one-shot service invocations. Every ready job has an
// independent deadline and runs concurrently, so one blocked model call cannot
// prevent another job from applying its result.
func (w Worker) Once(ctx context.Context) error {
	work, err := w.Reader.Ready(ctx, time.Now())
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for _, item := range work {
		wg.Go(func() {
			if err := w.Step(ctx, item); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(failures...)
}

// Run keeps at most one active step per job and schedules from persisted policy.
// Step errors stop only that attempt; polling re-reads the durable watermark.
// A snapshot failure stops the service visibly, canceling and joining active
// work. A Core reservation with an unknown outcome is never reissued by Aux.
func (w Worker) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return jobapi.ErrInvalid
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	var mu sync.Mutex
	active := map[string]bool{}
	for {
		work, err := w.Reader.Ready(ctx, time.Now())
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		for _, item := range work {
			mu.Lock()
			running := active[item.Job.ID]
			if !running {
				active[item.Job.ID] = true
			}
			mu.Unlock()
			if running {
				continue
			}
			wg.Go(func() {
				defer func() {
					mu.Lock()
					delete(active, item.Job.ID)
					mu.Unlock()
				}()
				_ = w.Step(ctx, item)
			})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
