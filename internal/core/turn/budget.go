// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"errors"
	"sync"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

const BudgetReached loop.Code = "budget_reached"

var (
	ErrReportTools = errors.New("report_tools_forbidden")
	errPromote     = errors.New("turn_requires_promotion")
)

type budgetContextKey struct{}

type turnBudget struct {
	mu        sync.Mutex
	gauge     *budget.Gauge
	remaining int
	effects   []budget.Effect
}

func recordEffect(ctx context.Context, effect budget.Effect) {
	if b, ok := ctx.Value(budgetContextKey{}).(*turnBudget); ok {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.effects = append(b.effects, effect)
	}
}

func (b *turnBudget) configure(cfg loop.Config, report bool) loop.Config {
	transform := cfg.Transform
	cfg.Transform = func(ctx context.Context, req provider.Request) (provider.Request, error) {
		if !report && b.gauge.Snapshot().Level == budget.Hard {
			return req, errPromote
		}
		var err error
		if transform != nil {
			req, err = transform(ctx, req)
		}
		if report {
			req.Tools = nil
		}
		return req, err
	}
	calls, result := cfg.Journal.Calls, cfg.Journal.Result
	cfg.Journal.Calls = func(ctx context.Context, round int, batch []provider.ToolCall) error {
		if err := calls(ctx, round, batch); err != nil {
			return err
		}
		if report {
			// A provider violating the no-tools request gets no second request.
			// Close every call durably so recovery cannot mistake it for dispatch.
			var err error
			for i, c := range batch {
				err = errors.Join(err, result(ctx, round, i, loop.Result{ID: c.ID, Code: loop.Denied}))
			}
			return errors.Join(ErrReportTools, err)
		}
		b.remaining = len(batch)
		return nil
	}
	cfg.Journal.Result = func(ctx context.Context, round, index int, r loop.Result) error {
		if err := result(ctx, round, index, r); err != nil {
			return err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.remaining--
		if b.remaining == 0 {
			b.gauge.Round()
		}
		return nil
	}
	blocked := func(name string) bool {
		return name != "start_run" && name != "dispatch_work" && b.gauge.Snapshot().Level != budget.Under
	}
	before := cfg.Before
	cfg.Before = func(ctx context.Context, c provider.ToolCall) *loop.Result {
		if blocked(c.Name) {
			return &loop.Result{ID: c.ID, Code: BudgetReached}
		}
		if before != nil {
			return before(ctx, c)
		}
		return nil
	}
	wrapped := make(map[string]loop.Tool, len(cfg.Tools))
	for name, tool := range cfg.Tools {
		if tool.Validate == nil || tool.Execute == nil {
			wrapped[name] = tool
			continue
		}
		wrapped[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				if blocked(c.Name) {
					return "", nil // typed budget refusal precedes schema validation
				}
				return tool.Validate(c)
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				end := b.gauge.Tool()
				defer end()
				return tool.Execute(ctx, c)
			},
		}
	}
	cfg.Tools = wrapped
	return cfg
}
