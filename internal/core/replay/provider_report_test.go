// SPDX-License-Identifier: Apache-2.0
package replay

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/kit/provider"
	"io"
	"strings"
	"testing"
	"time"
)

type reportStream struct {
	events  []provider.Event
	meta    provider.Meta
	failure error
}

func (s *reportStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		if s.failure != nil {
			return provider.Event{}, s.failure
		}
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (s *reportStream) Meta() provider.Meta { return s.meta }
func (s *reportStream) Close() error        { return nil }
func count64(n int64) *int64                { return &n }
func TestProviderReportMeasuresActualOutputAndUnknownUsage(t *testing.T) {
	now := time.Unix(0, 0)
	report := ProviderReport{Version: 1}
	stream := report.Observe("turn-1", 1, "responses_api_key", func() time.Time { return now }, func() (provider.Stream, error) {
		now = now.Add(7 * time.Millisecond)
		return &reportStream{events: []provider.Event{{Kind: provider.ItemDone, Item: []byte(`{"type":"reasoning"}`)}, {Kind: provider.Delta, Delta: ""}, {Kind: provider.ToolCallReady, Call: &provider.ToolCall{ID: "call"}}, {Kind: provider.Terminal}}, meta: provider.Meta{Usage: provider.Usage{Input: count64(100), Cached: count64(70)}}}, nil
	})
	for i := 0; i < 4; i++ {
		now = now.Add(3 * time.Millisecond)
		if _, err := stream.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	stream.Close()
	row := report.Requests[0]
	if row.FirstOutputMS == nil || *row.FirstOutputMS != 16 {
		t.Fatalf("time includes transport, skips reasoning/empty delta: %+v", row)
	}
	if row.Plain == nil || *row.Plain != 30 || row.Written != nil || row.WrittenByLifetime != nil {
		t.Fatalf("unknown writes must remain unknown: %+v", row)
	}
}
func TestProviderReportFailureAndLifetimeAccounting(t *testing.T) {
	report := ProviderReport{Version: 1}
	s := report.Observe("turn-1", 1, "claude", time.Now, func() (provider.Stream, error) {
		return &reportStream{meta: provider.Meta{Usage: provider.Usage{Input: count64(100), Cached: count64(40), CacheWrite: count64(20), CacheByLifetime: map[provider.CacheLifetime]provider.CacheUsage{provider.Cache1Hour: {Write: count64(20)}, provider.Cache5Minutes: {Read: count64(4)}}}}, failure: errors.New("private provider body")}, nil
	})
	if _, err := s.Next(context.Background()); err == nil {
		t.Fatal("missing failure")
	}
	s.Close()
	r := report.Requests[0]
	if r.FirstOutputMS != nil || r.Plain == nil || *r.Plain != 100 || r.Input == nil || *r.Input != 160 || r.WrittenByLifetime[provider.Cache5Minutes] != nil || r.Failure != "stream_failed" {
		t.Fatalf("row: %+v", r)
	}
	empty := ProviderReport{Version: 1}
	x := empty.Observe("turn", 1, "claude", time.Now, func() (provider.Stream, error) { return nil, errors.New("secret") })
	_, _ = x.Next(context.Background())
	x.Close()
	if empty.Requests[0].Input != nil || empty.Requests[0].Plain != nil || empty.Requests[0].Failure != "start_failed" {
		t.Fatal(empty)
	}
}

func TestProviderReportClaudeAPIKeyPlainInput(t *testing.T) {
	report := ProviderReport{Version: 1}
	s := report.Observe("turn-1", 1, "claude_api_key", time.Now, func() (provider.Stream, error) {
		return &reportStream{meta: provider.Meta{Usage: provider.Usage{Input: count64(100), Cached: count64(40), CacheWrite: count64(20), CacheByLifetime: map[provider.CacheLifetime]provider.CacheUsage{provider.Cache1Hour: {Write: count64(20)}}}}, events: []provider.Event{{Kind: provider.Terminal}}}, nil
	})
	s.Next(context.Background())
	s.Close()
	r := report.Requests[0]
	if r.Input == nil || *r.Input != 160 || r.Plain == nil || *r.Plain != 100 || r.Read == nil || *r.Read != 40 || r.Written == nil || *r.Written != 20 {
		t.Fatalf("row: %+v", r)
	}
	if write := r.WrittenByLifetime[provider.Cache1Hour]; write == nil || *write != 20 {
		t.Fatalf("lifetime writes: %+v", r.WrittenByLifetime)
	}
	if r.CacheShare == nil || *r.CacheShare != 0.25 {
		t.Fatalf("cache share: %+v", r.CacheShare)
	}
}

func TestProviderCompareAlignsMissingRequestsAndRejectsDifferentSources(t *testing.T) {
	a := ProviderReport{Version: 1, Source: "same", Requests: []RequestReport{{Turn: "1", Round: 1, Input: count64(9)}}}
	b := ProviderReport{Version: 1, Source: "same", Requests: []RequestReport{{Turn: "1", Round: 1, Input: count64(8)}, {Turn: "1", Round: 2}}}
	var out strings.Builder
	if err := Compare(&out, a, b); err != nil || !strings.Contains(out.String(), "9 / 8") || !strings.Contains(out.String(), "missing") || !strings.Contains(out.String(), "unknown") {
		t.Fatal(out.String(), err)
	}
	b.Source = "different"
	if err := Compare(io.Discard, a, b); err == nil {
		t.Fatal("compared different inputs")
	}
}
