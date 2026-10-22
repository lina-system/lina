// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/thisisjun786/lina/kit/provider"
)

type runtimeBridge struct {
	mu      sync.Mutex
	cfg     Config
	round   int
	offered map[string]bool
	seen    map[string]bool
	results []Result
}

func (b *runtimeBridge) Call(ctx context.Context, call provider.ToolCall) (provider.ToolResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if call.ID == "" || call.Name == "" || b.seen[call.ID] {
		return provider.ToolResult{}, ErrResponse
	}
	b.seen[call.ID] = true
	if b.cfg.Journal.Calls != nil {
		if err := b.cfg.Journal.Calls(ctx, b.round, []provider.ToolCall{call}); err != nil {
			return provider.ToolResult{}, err
		}
	}
	// Keep each runtime call's result index distinct in the same round.
	cfg := b.cfg
	index := len(b.results)
	cfg.Journal.Result = func(ctx context.Context, round, _ int, result Result) error {
		if b.cfg.Journal.Result == nil {
			return nil
		}
		return b.cfg.Journal.Result(ctx, round, index, result)
	}
	results, err := execute(ctx, cfg, b.round, []provider.ToolCall{call}, b.offered)
	if len(results) == 0 {
		return provider.ToolResult{}, err
	}
	b.results = append(b.results, results...)
	output, marshalErr := json.Marshal(results[0])
	if err == nil {
		err = marshalErr
	}
	return provider.ToolResult{ID: call.ID, Output: output}, err
}
