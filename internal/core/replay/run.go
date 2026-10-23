// SPDX-License-Identifier: Apache-2.0

// Package replay runs the existing stateless loop with read-only dependencies.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/thisisjun786/lina/internal/core/toolcatalog"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

var ErrExcluded = errors.New("recorded_result_unavailable")
var ErrInvalid = errors.New("invalid_replay")

type Call struct {
	EffectKey string            `json:"effect_key,omitempty"`
	Call      provider.ToolCall `json:"call"`
	Result    *loop.Result      `json:"result"`
}

type RunConfig struct {
	Request provider.Request
	Calls   []Call
	Stream  loop.StreamFunc
	// Only trusted local read implementations may be supplied. There is no
	// effect executor, registry, canon writer, or default dispatch fallback.
	Reads map[string]loop.Tool
}

func callKey(call provider.ToolCall) (string, error) {
	args := call.Arguments
	if !call.Custom {
		dec := json.NewDecoder(bytes.NewBufferString(args))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			return "", err
		}
		if dec.Decode(new(any)) != io.EOF {
			return "", ErrInvalid
		}
		data, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		args = string(data)
	}
	raw, err := json.Marshal([]any{call.Name, call.Custom, args})
	return string(raw), err
}

func usable(result *loop.Result) bool {
	return result != nil && result.Code != loop.Unknown && result.Code != loop.AwaitingPerson && result.Code != loop.Aborted && result.Code != loop.Incomplete
}

// Run cannot mutate Core: the only injectable tool dependency is a local read.
// Calls match name, argument bytes (canonical JSON for functions), and order,
// never a candidate model's newly generated call id alone.
func Run(ctx context.Context, cfg RunConfig) (loop.Outcome, error) {
	recorded := make(map[string][]loop.Result)
	for _, call := range cfg.Calls {
		if _, live := cfg.Reads[call.Call.Name]; live {
			continue
		}
		if !usable(call.Result) {
			return loop.Outcome{}, ErrExcluded
		}
		key, err := callKey(call.Call)
		if err != nil {
			return loop.Outcome{}, ErrExcluded
		}
		recorded[key] = append(recorded[key], *call.Result)
	}
	all, err := recordedDeclarations(cfg.Calls)
	if err != nil {
		return loop.Outcome{}, err
	}
	catalog := &toolcatalog.Catalog{First: cfg.Request.Tools, All: all, Offset: len(cfg.Request.Items)}
	var mu sync.Mutex
	missing := false
	cfg.Request.Purpose = provider.Replay
	out, err := loop.Run(ctx, loop.Config{
		Request: cfg.Request, Stream: cfg.Stream, Tools: cfg.Reads,
		// Discarding Journal: no callback reaches turn.Store or the effect ledger.
		Journal: loop.Journal{}, Parallel: 1,
		Admit: func(_ context.Context, call provider.ToolCall) *loop.Result {
			if _, live := cfg.Reads[call.Name]; live {
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			key, err := callKey(call)
			if err == nil && len(recorded[key]) > 0 {
				r := recorded[key][0]
				recorded[key] = recorded[key][1:]
				r.ID = call.ID
				return &r
			}
			missing = true
			return &loop.Result{ID: call.ID, Code: loop.Unavailable}
		},
		Transform: func(ctx context.Context, r provider.Request) (provider.Request, error) {
			mu.Lock()
			defer mu.Unlock()
			if missing {
				return r, ErrExcluded
			}
			return catalog.Transform(ctx, r)
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if missing {
		return loop.Outcome{}, ErrExcluded
	}
	return out, err
}
