// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"

	"github.com/thisisjun786/lina/kit/provider"
)

// ProviderReport contains measurements only, never model inputs or outputs.
type ProviderReport struct {
	Version  int                 `json:"version"`
	Provider provider.ProviderID `json:"provider"`
	Route    string              `json:"route"`
	Model    provider.ModelRef   `json:"model"`
	Source   string              `json:"source"`
	Requests []RequestReport     `json:"requests"`
	Failure  string              `json:"failure,omitempty"`
}
type RequestReport struct {
	Turn              string                            `json:"turn"`
	Round             int                               `json:"round"`
	Input             *int64                            `json:"input"`
	Read              *int64                            `json:"read"`
	Written           *int64                            `json:"written"`
	WrittenByLifetime map[provider.CacheLifetime]*int64 `json:"written_by_lifetime"`
	Plain             *int64                            `json:"plain"`
	CacheShare        *float64                          `json:"cache_share"`
	FirstOutputMS     *float64                          `json:"first_output_ms"`
	Failure           string                            `json:"failure,omitempty"`
}

// Observe starts the clock before opening the transport. Each stream must be
// closed before the next is observed; even failed starts have one report row.
func (r *ProviderReport) Observe(turn string, round int, route string, now func() time.Time, start func() (provider.Stream, error), bridges ...*provider.ToolBridge) provider.Stream {
	index := len(r.Requests)
	r.Requests = append(r.Requests, RequestReport{Turn: turn, Round: round})
	s := &measuredStream{row: &r.Requests[index], route: route, now: now, started: now()}
	for _, b := range bridges {
		if b != nil && *b != nil {
			*b = &measuredBridge{stream: s, bridge: *b}
		}
	}
	s.stream, s.err = start()
	if s.err != nil || s.stream == nil {
		s.row.Failure = "start_failed"
		if s.err == nil {
			s.err = ErrInvalid
		}
	}
	return s
}

type measuredStream struct {
	stream   provider.Stream
	row      *RequestReport
	route    string
	now      func() time.Time
	started  time.Time
	err      error
	closed   bool
	terminal bool
}

func (s *measuredStream) Next(ctx context.Context) (provider.Event, error) {
	if s.err != nil {
		return provider.Event{}, s.err
	}
	e, err := s.stream.Next(ctx)
	if err != nil && (!errors.Is(err, io.EOF) || !s.terminal) {
		s.row.Failure = "stream_failed"
	}
	if err == nil && e.Kind == provider.Terminal {
		s.terminal = true
	}
	if err == nil && s.row.FirstOutputMS == nil && outputEvent(e) {
		v := float64(s.now().Sub(s.started)) / float64(time.Millisecond)
		s.row.FirstOutputMS = &v
	}
	return e, err
}
func outputEvent(e provider.Event) bool {
	if e.Kind == provider.Delta {
		return e.Delta != ""
	}
	if e.Kind == provider.ToolCallReady {
		return e.Call != nil
	}
	if e.Kind != provider.ItemDone {
		return false
	}
	var item struct {
		Type, Role string
		Content    []struct{ Type, Text string }
	}
	if json.Unmarshal(e.Item, &item) != nil || item.Role != "assistant" {
		return false
	}
	for _, c := range item.Content {
		if (c.Type == "output_text" || c.Type == "text") && c.Text != "" {
			return true
		}
	}
	return false
}
func (s *measuredStream) Meta() provider.Meta {
	if s.stream == nil {
		return provider.Meta{}
	}
	return s.stream.Meta()
}

// Reject fails the row a stream is measuring without discarding its tokens. A
// caller that drives a stream itself uses it when a completed response breaks
// the request's purpose, such as a compaction summary that calls a tool.
func Reject(stream provider.Stream, reason string) {
	if s, ok := stream.(*measuredStream); ok && s.row.Failure == "" {
		s.row.Failure = reason
	}
}
func (s *measuredStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.stream == nil {
		return nil
	}
	u := s.stream.Meta().Usage
	s.row.Input, s.row.Read, s.row.Written = u.Input, u.Cached, u.CacheWrite
	if u.CacheByLifetime != nil {
		s.row.WrittenByLifetime = map[provider.CacheLifetime]*int64{}
		for k, v := range u.CacheByLifetime {
			s.row.WrittenByLifetime[k] = v.Write
		}
	}
	// Claude reports ordinary-rate input separately. Responses includes cache
	// reads in input, and its cache writes incur no separate input rate.
	switch s.route {
	case "claude", "claude_api_key":
		s.row.Plain = u.Input
		s.row.Input = nil
		if u.Input != nil && u.Cached != nil && u.CacheWrite != nil && *u.Input <= math.MaxInt64-*u.Cached && *u.Input+*u.Cached <= math.MaxInt64-*u.CacheWrite {
			v := *u.Input + *u.Cached + *u.CacheWrite
			s.row.Input = &v
		}
	case "responses_api_key", "chatgpt":
		if u.Input != nil && u.Cached != nil && *u.Input >= *u.Cached {
			v := *u.Input - *u.Cached
			s.row.Plain = &v
		}
	}
	// Cache share is the reported read tokens over all input. Unknown input or
	// an unreported read leaves it unknown rather than zero.
	if s.row.Input != nil && s.row.Read != nil && *s.row.Input > 0 {
		v := float64(*s.row.Read) / float64(*s.row.Input)
		s.row.CacheShare = &v
	}
	err := s.stream.Close()
	if err != nil {
		s.row.Failure = "close_failed"
	}
	return err
}

// Runtime tool calls may arrive through the bridge without a stream event.
type measuredBridge struct {
	stream *measuredStream
	bridge provider.ToolBridge
}

func (b *measuredBridge) Call(ctx context.Context, call provider.ToolCall) (provider.ToolResult, error) {
	if b.stream.row.FirstOutputMS == nil {
		v := float64(b.stream.now().Sub(b.stream.started)) / float64(time.Millisecond)
		b.stream.row.FirstOutputMS = &v
	}
	return b.bridge.Call(ctx, call)
}
