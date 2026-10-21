// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/thisisjun786/lina/kit/provider"
)

func saveResult(ctx context.Context, j Journal, round, index int, r Result) error {
	if j.Result == nil {
		return nil
	}
	save, cancel := cleanupContext(ctx)
	defer cancel()
	return j.Result(save, round, index, r)
}
func admission(ctx context.Context, cfg Config, c provider.ToolCall) *Result {
	if cfg.Admit == nil {
		return nil
	}
	r := cfg.Admit(ctx, c)
	if r == nil {
		return nil
	}
	result := *r
	result.ID = c.ID
	if result.Code == "" {
		result.Code = Denied
	}
	return &result
}

func invoke(ctx context.Context, cfg Config, tool Tool, c provider.ToolCall) (r Result) {
	r = Result{ID: c.ID, Code: Unknown}
	defer func() {
		if recover() != nil {
			r = Result{ID: c.ID, Code: Unknown}
		}
	}()
	if denied := admission(ctx, cfg, c); denied != nil {
		r = *denied
	} else {
		r = tool.Execute(ctx, c)
		if cfg.After != nil {
			r = cfg.After(ctx, c, r)
		}
	}
	r.ID = c.ID
	if len(r.Output) > 0 && !json.Valid(r.Output) {
		return Result{ID: c.ID, Code: Unknown}
	}
	switch r.Code {
	case OK, Invalid, Denied, Unavailable, Aborted, Incomplete, AwaitingPerson, Failed, Unknown, BudgetReached:
	default:
		r = Result{ID: c.ID, Code: Unknown}
	}
	return r
}
func execute(ctx context.Context, cfg Config, round int, calls []provider.ToolCall, offered map[string]bool) ([]Result, error) {
	results := make([]Result, len(calls))
	blocked := make([]bool, len(calls))
	targets := make([]string, len(calls))
	// Validate and preflight every call in order before any executor starts.
	for i, c := range calls {
		r := Result{ID: c.ID}
		t, ok := cfg.Tools[c.Name]
		if denied := admission(ctx, cfg, c); denied != nil {
			r = *denied
		} else if !ok || !offered[c.Name] || t.Validate == nil || t.Execute == nil {
			r.Code = Unavailable
		} else if target, err := t.Validate(c); err != nil {
			r.Code = Invalid
		} else {
			targets[i] = target
			if cfg.Before != nil {
				if denied := cfg.Before(ctx, c); denied != nil {
					r = *denied
					r.ID = c.ID
					if r.Code == "" {
						r.Code = Denied
					}
				}
			}
		}
		if r.Code != "" {
			blocked[i] = true
			results[i] = r
		}
	}
	parallel := cfg.Parallel
	if parallel == 0 || parallel > 8 {
		parallel = 8
	}
	slots := make(chan struct{}, parallel)
	last := map[string]chan struct{}{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	for i, c := range calls {
		done := make(chan struct{})
		previous := last[targets[i]]
		if targets[i] != "" {
			last[targets[i]] = done
		} else {
			previous = nil
		}
		wg.Go(func() {
			defer close(done)
			r := results[i]
			// Only calls that never enter invoke receive a synthetic abort. Once
			// dispatched, its returned result describes the committed receipt.
			if !blocked[i] {
				r = Result{ID: c.ID, Code: Aborted}
				if previous != nil {
					select {
					case <-previous:
					case <-runCtx.Done():
					}
				}
				if runCtx.Err() == nil {
					select {
					case slots <- struct{}{}:
						if runCtx.Err() == nil {
							r = invoke(runCtx, cfg, cfg.Tools[c.Name], c)
						}
						<-slots
					case <-runCtx.Done():
					}
				}
			}
			results[i] = r
			if err := saveResult(ctx, cfg.Journal, round, i, r); err != nil {
				mu.Lock()
				failure = errors.Join(failure, err)
				mu.Unlock()
				cancel(err)
			}
		})
	}
	wg.Wait()
	return results, errors.Join(failure, context.Cause(runCtx))
}
