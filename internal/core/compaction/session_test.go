// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestLoopTransformBlocksDispatchOnSummaryFailure(t *testing.T) {
	p, v := fixturePass()
	failed := errors.New("summary failed")
	p.Summarize = func(context.Context, PassSegment) (Text, error) { return Text{}, failed }
	s, err := NewSession(p, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = loop.Run(t.Context(), loop.Config{Request: v.request(), Transform: s.Transform, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		calls++
		return nil, errors.New("unexpected dispatch")
	}})
	if !errors.Is(err, failed) || calls != 0 || !reflect.DeepEqual(s.View().History, v.History) {
		t.Fatalf("dispatch=%d err=%v", calls, err)
	}
}
func TestSessionPreservesCompactedPrefixAcrossAppends(t *testing.T) {
	p, v := fixturePass()
	calls := 0
	p.Summarize = func(context.Context, PassSegment) (Text, error) {
		calls++
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, nil
	}
	p.Publish = func(context.Context, PassSegment, Text) (Published, error) {
		return Published{ID: "root", Item: message("summary")}, nil
	}
	s, err := NewSession(p, v, func(_ context.Context, items []provider.Item) ([]Record, error) {
		return []Record{{ID: "appended", Item: items[0]}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Transform(t.Context(), v.request())
	if err != nil {
		t.Fatal(err)
	}
	next := v.request()
	next.Items = append(next.Items, message("steering"))
	second, err := s.Transform(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(first.Items, second.Items[:len(second.Items)-1]) || !reflect.DeepEqual(second.Items[len(second.Items)-1], message("steering")) {
		t.Fatalf("changed prefix calls=%d", calls)
	}
}

func TestFollowUpRequestKeepsTheOrdinaryTrigger(t *testing.T) {
	p, v := fixturePass()
	p.Cold = true
	p.Policy.EarlyPercent = 70
	s, err := NewSession(p, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sizes, err := s.Sizes(); err != nil || sizes.Trigger != sizes.Threshold {
		t.Fatalf("follow-up request kept the early boundary: %+v %v", sizes, err)
	}
}
