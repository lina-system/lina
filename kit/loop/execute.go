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

func invoke(ctx context.Context, cfg Config, tool Tool, round, index int, c provider.ToolCall) (r Result, err error) {
	r = Result{ID: c.ID, Code: Unknown}
	defer func() {
		if recover() != nil {
			r = Result{ID: c.ID, Code: Unknown}
		}
	}()
	if denied := admission(ctx, cfg, c); denied != nil {
		r = *denied
	} else {
		if cfg.Journal.Start != nil {
			if err := cfg.Journal.Start(ctx, round, index, c); err != nil {
				return Result{ID: c.ID, Code: Aborted}, err
			}
		}
		r = tool.Execute(ctx, c)
		if cfg.After != nil {
			r = cfg.After(ctx, c, r)
		}
	}
	r.ID = c.ID
	if len(r.Output) > 0 && !json.Valid(r.Output) {
		return Result{ID: c.ID, Code: Unknown}, nil
	}
	switch r.Code {
	case OK, Invalid, Denied, Unavailable, Aborted, Incomplete, AwaitingPerson, Failed, Unknown, BudgetReached:
	default:
		r = Result{ID: c.ID, Code: Unknown}
	}
	return r, nil
}
func execute(ctx context.Context, cfg Config, round int, calls []provider.ToolCall, offered map[string]bool) ([]Result, error) {
	results := make([]Result, len(calls))
	blocked := make([]bool, len(calls))
	targets := make([]string, len(calls))
	gates := make([]string, len(calls))
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
			if t.Gate != nil {
				gates[i] = t.Gate(c)
			}
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
	done := make([]chan struct{}, len(calls))
	for i := range done {
		done[i] = make(chan struct{})
	}
	// Calls with the same write target form a chain in call order. A gate names
	// another call's write target; its call waits for that call even when it
	// appears later in the response.
	previous := make([]chan struct{}, len(calls))
	for i := range calls {
		if targets[i] == "" {
			continue
		}
		previous[i] = last[targets[i]]
		last[targets[i]] = done[i]
	}
	gate := make([]chan struct{}, len(calls))
	for i := range calls {
		if gates[i] == "" || gates[i] == targets[i] {
			continue
		}
		gate[i] = last[gates[i]]
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failure error
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	fail := func(err error) {
		mu.Lock()
		failure = errors.Join(failure, err)
		mu.Unlock()
		cancel(err)
	}
	for i, c := range calls {
		wg.Go(func() {
			defer close(done[i])
			r := results[i]
			// Only calls that never reach Execute receive a synthetic abort. Once
			// dispatched, its returned result describes the committed receipt.
			if !blocked[i] {
				r = Result{ID: c.ID, Code: Aborted}
				if previous[i] != nil {
					select {
					case <-previous[i]:
					case <-runCtx.Done():
					}
				}
				if gate[i] != nil {
					select {
					case <-gate[i]:
					case <-runCtx.Done():
					}
				}
				if runCtx.Err() == nil {
					select {
					case slots <- struct{}{}:
						if runCtx.Err() == nil {
							var err error
							r, err = invoke(runCtx, cfg, cfg.Tools[c.Name], round, i, c)
							if err != nil {
								fail(err)
							}
						}
						<-slots
					case <-runCtx.Done():
					}
				}
			}
			results[i] = r
			if err := saveResult(ctx, cfg.Journal, round, i, r); err != nil {
				fail(err)
			}
		})
	}
	wg.Wait()
	return results, errors.Join(failure, context.Cause(runCtx))
}
