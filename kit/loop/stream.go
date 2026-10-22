// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/thisisjun786/lina/kit/provider"
)

type batch struct {
	items   []provider.Item
	calls   []provider.ToolCall
	meta    provider.Meta
	results []Result
}

func collect(ctx context.Context, cfg Config, round int, req provider.Request, seen map[string]bool) (out batch, err error) {
	bridge := &runtimeBridge{cfg: cfg, round: round, offered: offered(req), seen: seen}
	if cfg.RuntimeTools {
		req.Bridge = bridge
	}
	defer func() { out.results = bridge.results }()
	stream, err := cfg.Stream(ctx, req)
	if err != nil {
		return out, err
	}
	if stream == nil {
		return out, ErrResponse
	}
	items := map[int64]provider.Item{}
	calls := map[int64]provider.ToolCall{}
	ready := map[int64]bool{}
	itemCalls := map[int64]provider.ToolCall{}
	partials := map[int64]string{}
	terminal := false
	defer func() {
		out.meta = stream.Meta()
		err = errors.Join(err, stream.Close())
		indices := make([]int64, 0, len(items))
		for i := range items {
			indices = append(indices, i)
		}
		sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
		for _, i := range indices {
			out.items = append(out.items, items[i])
		}
		indices = indices[:0]
		for i := range calls {
			indices = append(indices, i)
		}
		sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
		ids := map[string]bool{}
		for _, i := range indices {
			c := calls[i]
			if ids[c.ID] {
				err = errors.Join(err, ErrResponse)
				continue
			}
			ids[c.ID] = true
			out.calls = append(out.calls, c)
		}
		if err != nil {
			err = errors.Join(ErrIncomplete, err)
			if cfg.Journal.Partial != nil {
				save, cancel := cleanupContext(ctx)
				defer cancel()
				for i, text := range partials {
					err = errors.Join(err, cfg.Journal.Partial(save, round, i, text))
				}
			}
		}
	}()
	for {
		e, nextErr := stream.Next(ctx)
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) && terminal {
				return out, context.Cause(ctx)
			}
			return out, errors.Join(ErrIncomplete, nextErr)
		}
		if terminal || e.Index < 0 {
			return out, ErrResponse
		}
		switch e.Kind {
		case provider.Delta:
			partials[e.Index] += e.Delta
		case provider.ItemDone:
			if _, exists := items[e.Index]; exists || !json.Valid(e.Item) {
				return out, ErrResponse
			}
			items[e.Index] = append(provider.Item(nil), e.Item...)
			if cfg.Journal.Item != nil {
				if err := cfg.Journal.Item(ctx, round, e.Index, items[e.Index]); err != nil {
					return out, err
				}
			}
			var item struct {
				Type, Name, Arguments, Input string
				ID                           string `json:"call_id"`
			}
			if json.Unmarshal(e.Item, &item) != nil {
				return out, ErrResponse
			}
			if item.Type == "function_call" || item.Type == "custom_tool_call" {
				c := provider.ToolCall{ID: item.ID, Name: item.Name, Arguments: item.Arguments, Custom: item.Type == "custom_tool_call"}
				if c.Custom {
					c.Arguments = item.Input
				}
				if c.ID == "" || c.Name == "" {
					return out, ErrResponse
				}
				if prior, ok := calls[e.Index]; ok && prior != c {
					return out, ErrResponse
				}
				calls[e.Index] = c
				itemCalls[e.Index] = c
			}
		case provider.ToolCallReady:
			if ready[e.Index] || e.Call == nil || e.Call.ID == "" || e.Call.Name == "" {
				return out, ErrResponse
			}
			if prior, ok := calls[e.Index]; ok && prior != *e.Call {
				return out, ErrResponse
			}
			calls[e.Index] = *e.Call
			ready[e.Index] = true
		case provider.Terminal:
			for index := range calls {
				if call, ok := itemCalls[index]; !ok || call != calls[index] {
					return out, ErrResponse
				}
			}
			terminal = true
		default:
			return out, ErrResponse
		}
		if cfg.Observe != nil {
			if err := cfg.Observe(ctx, e); err != nil {
				return out, err
			}
		}
	}
}
