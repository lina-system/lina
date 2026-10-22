// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// ToolActivity observes invocation boundaries without arguments or output.
// ToolName is the raw registered name; clients use tools.DisplayName for labels.
// Code and Duration are populated only on finish. Duration uses monotonic time
// from the start notification through the durable result commit.
type ToolActivity struct {
	CallID, ToolName string
	Started          bool
	Code             loop.Code
	Duration         time.Duration
}

// observeTools wraps the committed result, including budget accounting and
// result notifications, so observer errors never undo those consequences.
func observeTools(j loop.Journal, observe func(context.Context, ToolActivity) error) loop.Journal {
	if observe == nil {
		return j
	}
	type invocation struct {
		name string
		at   time.Time
	}
	var mu sync.Mutex
	started := make(map[string]invocation)
	j.Start = func(ctx context.Context, _, _ int, c provider.ToolCall) error {
		mu.Lock()
		started[c.ID] = invocation{name: c.Name, at: time.Now()}
		mu.Unlock()
		return observe(ctx, ToolActivity{CallID: c.ID, ToolName: c.Name, Started: true})
	}
	result := j.Result
	j.Result = func(ctx context.Context, round, index int, r loop.Result) error {
		if err := result(ctx, round, index, r); err != nil {
			return err
		}
		mu.Lock()
		start, ok := started[r.ID]
		delete(started, r.ID)
		mu.Unlock()
		if !ok {
			return nil
		}
		// The loop supplies its bounded cleanup context, including on cancellation.
		return observe(ctx, ToolActivity{CallID: r.ID, ToolName: start.name, Code: r.Code, Duration: time.Since(start.at)})
	}
	return j
}
