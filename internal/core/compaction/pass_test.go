// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func message(text string) provider.Item { return textItem("user", text) }
func countBytes(r provider.Request) int {
	n := len(r.Instructions)
	for _, list := range [][]provider.Item{r.Tools, r.Items} {
		for _, item := range list {
			n += len(item)
		}
	}
	return n
}
func fixturePass() (Pass, View) {
	p := Pass{Policy: Policy{ContextWindow: 2000, TriggerPercent: 90, KeepToolTokens: 100, KeepUserTokens: 100}, Count: countBytes}
	v := View{Request: provider.Request{Model: "model", Purpose: provider.Turn}, Fixed: []provider.Item{message("fixed")}, Packet: []provider.Item{message("packet")}, Input: Record{ID: "input", Item: message("question"), User: true}, History: []Record{{ID: "old", Item: message(strings.Repeat("x", 2200)), User: true}}}
	return p, v
}
func TestFailedSummaryKeepsEventsAndPublishesNothing(t *testing.T) {
	p, v := fixturePass()
	failed := errors.New("summarizer unavailable")
	p.Summarize = func(context.Context, PassSegment) (Text, error) { return Text{}, failed }
	published := 0
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		published++
		return Published{ID: "root", Item: message("summary")}, nil
	}
	got, err := p.Fit(t.Context(), v, false)
	if !errors.Is(err, failed) || published != 0 || !reflect.DeepEqual(got.View.History, v.History) || len(got.Request.Items) != 0 {
		t.Fatalf("failure lost history or dispatched: %+v %v publications=%d", got, err, published)
	}
}
func TestStubsPrecedeSummaryAndPreserveCurrentRoundTrip(t *testing.T) {
	p, v := fixturePass()
	v.History = []Record{{ID: "call", Item: provider.Item(`{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"}`)}, {ID: "output", Item: provider.Item(`{"type":"function_call_output","call_id":"c","output":"` + strings.Repeat("x", 2200) + `"}`), Note: "read_file completed"}, {ID: "tail", Item: message(strings.Repeat("t", 120)), User: true}}
	v.Active = []Record{{ID: "reason", Item: provider.Item(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
	p.Summarize = func(context.Context, PassSegment) (Text, error) {
		t.Fatal("summary before deterministic fit")
		return Text{}, nil
	}
	got, err := p.Fit(t.Context(), v, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.View.Active, v.Active) || !reflect.DeepEqual(got.View.Packet, v.Packet) || !reflect.DeepEqual(got.View.History[0], v.History[0]) || !strings.Contains(string(got.View.History[1].Item), "output") || !strings.Contains(string(got.View.History[1].Item), "expand") {
		t.Fatalf("bad stub projection: %+v", got.View)
	}
	tail := got.Request.Items[len(got.Request.Items)-3:]
	if !reflect.DeepEqual(tail, []provider.Item{v.Input.Item, v.Packet[0], v.Active[0].Item}) {
		t.Fatalf("compaction reordered input, packet or active items: %s", tail)
	}
	if !strings.Contains(string(v.History[1].Item), strings.Repeat("x", 2200)) {
		t.Fatal("mutated source")
	}
}
func TestOversizeContinuationRecordsSizesAndSendsNothing(t *testing.T) {
	p, v := fixturePass()
	v.Input.Item = message(strings.Repeat("q", 2200))
	p.Summarize = func(context.Context, PassSegment) (Text, error) {
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		return Published{ID: "root", Item: message("summary")}, nil
	}
	var steps []string
	var sizes Sizes
	c := Continuation{EndedTurn: "turn", InputID: "input", Record: func(context.Context, ContinuationRecord) error { steps = append(steps, "record"); return nil }, Packet: func(context.Context) ([]provider.Item, error) { steps = append(steps, "packet"); return v.Packet, nil }, Log: func(_ context.Context, s Sizes) error { sizes = s; steps = append(steps, "log"); return nil }, Send: func(context.Context, provider.Request) error { steps = append(steps, "send"); return nil }}
	_, err := p.Continue(t.Context(), v, c)
	if !errors.Is(err, ErrTooLarge) || !reflect.DeepEqual(steps, []string{"record", "packet", "log"}) {
		t.Fatalf("continuation dispatch: %v %v", steps, err)
	}
	if sizes.Total < sizes.Threshold || sizes.Window != 2000 || sizes.Fixed == 0 || sizes.Summary == 0 || sizes.Packet == 0 || sizes.Input == 0 {
		t.Fatalf("missing sizes: %+v", sizes)
	}
}

func TestInTurnPreparedThenWholeTailKeepsNewRootLineage(t *testing.T) {
	p, v := fixturePass()
	p.Policy.KeepUserTokens = 1000
	v.History = append(v.History, Record{ID: "tail", Item: message(strings.Repeat("t", 600)), User: true})
	v.Active = []Record{{ID: "active", Item: message(strings.Repeat("a", 900))}}
	prepared := 0
	published := 0
	p.Prepared = func(_ context.Context, s PassSegment) (Text, bool, error) {
		prepared++
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, true, nil
	}
	p.Publish = func(_ context.Context, s PassSegment, _ Text) (Published, error) {
		published++
		if published == 2 && (s.RootID != "first" || len(s.Records) != 1 || s.Records[0].ID != "tail") {
			t.Fatalf("lost parent lineage: %+v", s)
		}
		id := "first"
		if published == 2 {
			id = "second"
		}
		return Published{ID: id, Item: message(strings.Repeat("s", 300))}, nil
	}
	got, err := p.Fit(t.Context(), v, true)
	if err != nil || prepared != 2 || published != 2 || got.View.RootID != "second" || len(got.View.History) != 0 || !reflect.DeepEqual(got.View.Active, v.Active) || !reflect.DeepEqual(got.View.Input, v.Input) {
		t.Fatalf("bad in-turn stages: %+v %v prep=%d pub=%d", got, err, prepared, published)
	}
}
func TestContinuationInputOnceAndNoPriorRoundTrip(t *testing.T) {
	p, v := fixturePass()
	v.Active = []Record{{ID: "opaque", Item: provider.Item(`{"type":"reasoning","encrypted_content":"old-round"}`)}}
	p.Summarize = func(_ context.Context, s PassSegment) (Text, error) {
		if len(s.Records) != 2 || len(s.Verbatim) != 1 || s.Verbatim[0].ID != "input" {
			t.Fatalf("bad coverage: %+v", s)
		}
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		return Published{ID: "root", Item: message("summary")}, nil
	}
	sends := 0
	c := Continuation{EndedTurn: "ended", InputID: "input", Record: func(context.Context, ContinuationRecord) error { return nil }, Packet: func(context.Context) ([]provider.Item, error) { return []provider.Item{message("fresh-packet")}, nil }, Log: func(context.Context, Sizes) error { return nil }, Send: func(_ context.Context, r provider.Request) error {
		sends++
		count := 0
		for _, item := range r.Items {
			if reflect.DeepEqual(item, v.Input.Item) {
				count++
			}
			if strings.Contains(string(item), "old-round") {
				t.Fatal("old round survived")
			}
		}
		if count != 1 || !strings.Contains(string(r.Items[len(r.Items)-1]), "fresh-packet") {
			t.Fatalf("bad continuation layout: %s", r.Items)
		}
		return nil
	}}
	if _, err := p.Continue(t.Context(), v, c); err != nil || sends != 1 {
		t.Fatalf("%v sends=%d", err, sends)
	}
}

func TestCounterAlwaysReceivesConversationModel(t *testing.T) {
	p, v := fixturePass()
	p.Count = func(r provider.Request) int {
		if r.Model != "model" {
			t.Fatal("counter lost model")
		}
		return countBytes(r)
	}
	p.Summarize = func(context.Context, PassSegment) (Text, error) {
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		return Published{ID: "root", Item: message("summary")}, nil
	}
	if _, err := p.Fit(t.Context(), v, false); err != nil {
		t.Fatal(err)
	}
}

func TestBoundaryRetainsCompleteTurns(t *testing.T) {
	old := Record{ID: "old", Item: message(strings.Repeat("old ", 500)), User: true}
	recent := []Record{
		{ID: "recent", Item: message("read the catalog"), User: true},
		{ID: "call", Item: []byte(`{"type":"function_call","call_id":"read","name":"skill_index","arguments":"{}"}`)},
		{ID: "output", Item: []byte(`{"type":"function_call_output","call_id":"read","output":"catalog"}`)},
		{ID: "reply", Item: []byte(`{"role":"assistant","content":"Catalog read."}`)},
	}
	p := Pass{Policy: Policy{ContextWindow: 3000, KeepUserTokens: 600}, Count: countBytes}
	v := View{Request: provider.Request{Model: "model"}, History: append([]Record{old}, recent...), Input: Record{ID: "current", Item: message("continue"), User: true}}
	segment, err := p.boundarySegment(v)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(segment.Verbatim, recent) || !reflect.DeepEqual(segment.Records, []Record{old}) {
		t.Fatalf("retained tail split a turn: kept=%v records=%v", segment.Verbatim, segment.Records)
	}
	v.History[len(v.History)-1].Item = message(strings.Repeat("large reply ", 100))
	segment, err = p.boundarySegment(v)
	if err != nil || len(segment.Verbatim) != 0 || len(segment.Records) != len(v.History) {
		t.Fatalf("oversized turn was partially retained: %+v %v", segment, err)
	}
}

func TestOnePassRewritesHistoryOnce(t *testing.T) {
	p, v := fixturePass()
	p.Policy.KeepUserTokens = 100
	original := strings.Repeat("x", 2200)
	v.History = append(v.History,
		Record{ID: "call", Item: provider.Item(`{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"}`)},
		Record{ID: "output", Item: provider.Item(`{"type":"function_call_output","call_id":"c","output":"` + original + `"}`), Note: "read_file completed"},
	)
	p.Policy.KeepToolTokens = 1
	summarized, published := 0, 0
	p.Summarize = func(_ context.Context, s PassSegment) (Text, error) {
		summarized++
		// The summary reads the original output, never the deterministic note.
		raw, _ := json.Marshal(s.Records)
		if !strings.Contains(string(raw), original) {
			t.Fatal("summary lost the original tool output")
		}
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "detail"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		published++
		return Published{ID: "root", Item: message("summary")}, nil
	}
	out, err := p.Fit(t.Context(), v, false)
	if err != nil || summarized != 1 || published != 1 {
		t.Fatalf("pass rewrote history more than once: summaries=%d publications=%d err=%v", summarized, published, err)
	}
	if out.Sizes.Total >= out.Sizes.Threshold || len(out.Request.Items) != len(v.Fixed)+1+1+len(v.Packet) {
		t.Fatalf("final request did not rewrite the history once: %+v %d items", out.Sizes, len(out.Request.Items))
	}
	if !bytes.Equal(out.Request.Items[1], message("summary")) {
		t.Fatal("the rewritten request did not carry the new root")
	}
}
