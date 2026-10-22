// SPDX-License-Identifier: Apache-2.0

// Package loop implements the stateless conversation loop. Callers own admission,
// persistence, authorization, leases, and retries. Run never retries a stream.
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrIncomplete = errors.New("incomplete_response")
	ErrResponse   = errors.New("invalid_response")
	ErrConfig     = errors.New("invalid_loop_config")
)

type Code string

const (
	OK             Code = "ok"
	Invalid        Code = "invalid_arguments"
	Denied         Code = "denied"
	Unavailable    Code = "tool_unavailable"
	Aborted        Code = "aborted"
	Incomplete     Code = "incomplete_response"
	AwaitingPerson Code = "awaiting_person"
	Failed         Code = "failed"
	Unknown        Code = "unknown"
	BudgetReached  Code = "budget_reached"
)

type Result struct {
	ID     string          `json:"id"`
	Code   Code            `json:"code"`
	Output json.RawMessage `json:"output,omitempty"`
}

// Validate must be pure and return a normalized write target (empty for reads).
// Execute must respect cancellation; an executor with uncooperative adapters
// owns their leases and lifetimes even after returning an unknown result.
type Tool struct {
	Validate func(provider.ToolCall) (string, error)
	Execute  func(context.Context, provider.ToolCall) Result
}
type StreamFunc func(context.Context, provider.Request) (provider.Stream, error)

// Journal callbacks return only after commit. Items retain exact provider bytes;
// Calls commits the whole assistant call message before dispatch; Result commits
// on completion, although model context is assembled in original call order.
// Partial records aborted visible text outside model context.
type Journal struct {
	Item  func(context.Context, int, int64, provider.Item) error
	Calls func(context.Context, int, []provider.ToolCall) error
	// Start observes dispatch after Calls commits, queueing and final admission,
	// immediately before Execute. An error prevents execution, records an aborted
	// result and stops the run. Start and Result may run concurrently across calls.
	Start   func(context.Context, int, int, provider.ToolCall) error
	Result  func(context.Context, int, int, Result) error
	Partial func(context.Context, int, int64, string) error
}
type Config struct {
	Request provider.Request
	Stream  StreamFunc
	// Observe forwards streaming events; it must not execute tools.
	Observe   func(context.Context, provider.Event) error
	Tools     map[string]Tool
	Transform func(context.Context, provider.Request) (provider.Request, error)
	// Admit is a pure, concurrency-safe policy check before validation and again
	// immediately before queued execution. It must have no side effects.
	Admit    func(context.Context, provider.ToolCall) *Result
	Before   func(context.Context, provider.ToolCall) *Result
	After    func(context.Context, provider.ToolCall, Result) Result
	Inputs   InputQueue
	Journal  Journal
	Parallel int // zero selects eight; larger values are capped at eight
}
type Outcome struct {
	// FollowUps were accepted but not applied before this Run ended.
	FollowUps []provider.Item
	Items     []provider.Item
	Results   []Result
	Rounds    int
	Meta      provider.Meta
}

func cloneItems(items []provider.Item) []provider.Item {
	out := make([]provider.Item, len(items))
	for i, item := range items {
		out[i] = append(provider.Item(nil), item...)
	}
	return out
}
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}
func offered(r provider.Request) map[string]bool {
	names := map[string]bool{}
	for _, raw := range r.Tools {
		var d struct{ Name string }
		if json.Unmarshal(raw, &d) == nil && d.Name != "" {
			names[d.Name] = true
		}
	}
	return names
}

func Run(ctx context.Context, cfg Config) (out Outcome, err error) {
	out.Items = cloneItems(cfg.Request.Items)
	if cfg.Inputs == nil {
		cfg.Inputs = &Inputs{}
	}
	// Close admission and hand off anything accepted after the last drain on
	// every return path, including invalid configuration and canceled turns.
	defer func() { out.FollowUps = cfg.Inputs.Close() }()
	if cfg.Stream == nil || cfg.Parallel < 0 {
		return out, ErrConfig
	}
	seen := map[string]bool{}
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return out, context.Cause(ctx)
		}
		out.Items = append(out.Items, cfg.Inputs.Drain()...)
		req := cfg.Request
		req.Items = cloneItems(out.Items)
		req.Tools = cloneItems(req.Tools)
		var err error
		if cfg.Transform != nil {
			req, err = cfg.Transform(ctx, req)
			if err != nil {
				return out, err
			}
		}
		batch, err := collect(ctx, cfg, round, req)
		out.Rounds++
		out.Meta = batch.meta
		for _, c := range batch.calls {
			if seen[c.ID] {
				return out, errors.Join(ErrResponse, err)
			}
			seen[c.ID] = true
		}
		saveCtx, cancel := cleanupContext(ctx)
		if cfg.Journal.Calls != nil && len(batch.calls) > 0 {
			if e := cfg.Journal.Calls(saveCtx, round, batch.calls); e != nil {
				cancel()
				return out, errors.Join(err, e)
			}
		}
		cancel()
		if err != nil {
			code := Incomplete
			if ctx.Err() != nil {
				code = Aborted
			}
			results := make([]Result, len(batch.calls))
			for i, c := range batch.calls {
				results[i] = Result{ID: c.ID, Code: code}
				if e := saveResult(ctx, cfg.Journal, round, i, results[i]); e != nil {
					err = errors.Join(err, e)
				}
			}
			out.Results = append(out.Results, results...)
			return out, err
		}
		out.Items = append(out.Items, batch.items...)
		if len(batch.calls) == 0 {
			// Input admitted after this snapshot is returned by the deferred
			// close in FollowUps; later pushes retain ownership in the lane.
			steering := cfg.Inputs.Drain()
			if len(steering) == 0 {
				return out, nil
			}
			out.Items = append(out.Items, steering...)
			continue
		}
		results, err := execute(ctx, cfg, round, batch.calls, offered(req))
		out.Results = append(out.Results, results...)
		for i, r := range results {
			kind := "function_call_output"
			if batch.calls[i].Custom {
				kind = "custom_tool_call_output"
			}
			payload, _ := json.Marshal(r)
			item, _ := json.Marshal(struct {
				Type   string `json:"type"`
				ID     string `json:"call_id"`
				Output string `json:"output"`
			}{kind, r.ID, string(payload)})
			out.Items = append(out.Items, item)
		}
		if err != nil {
			return out, err
		}
	}
}
