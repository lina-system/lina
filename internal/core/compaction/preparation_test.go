// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPreparationRejectsChangedModelAndRootRevision(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"model", "root revision"} {
		t.Run(change, func(t *testing.T) {
			b, v, db := builderFixture(t)
			segment := PassSegment{Records: v.History, Protected: []Record{v.Input}}
			if change == "root revision" {
				text, err := b.Summarize(t.Context(), segment)
				if err != nil {
					t.Fatal(err)
				}
				root, err := b.Publish(t.Context(), segment, text)
				if err != nil {
					t.Fatal(err)
				}
				segment = PassSegment{RootID: root.ID, Root: root.Item, Records: []Record{v.Input}}
			}
			if _, err := b.Summarize(t.Context(), segment); err != nil {
				t.Fatal(err)
			}
			if change == "model" {
				b.Model = "different-model"
			} else {
				write(t, db, "UPDATE sum_node SET revision=revision+1 WHERE id=?", segment.RootID)
			}
			_, ready, err := b.Prepared(context.Background(), segment)
			if err != nil || ready {
				t.Fatalf("changed %s accepted: ready=%v err=%v", change, ready, err)
			}
		})
	}
}

func TestRootOnlyDemandBuildsHigherLevel(t *testing.T) {
	t.Parallel()
	b, v, _ := builderFixture(t)
	segment := PassSegment{Records: v.History, Protected: []Record{v.Input}}
	b.Generate = func(context.Context, PassSegment) (Text, error) {
		return Text{Requests: strings.Repeat("long ", 500), Topics: "topic", Actions: "none", Values: "42", Omitted: "wording"}, nil
	}
	text, err := b.Summarize(t.Context(), segment)
	if err != nil {
		t.Fatal(err)
	}
	root, err := b.Publish(t.Context(), segment, text)
	if err != nil {
		t.Fatal(err)
	}
	v.RootID, v.Root, v.History = root.ID, root.Item, nil
	calls := 0
	b.Generate = func(_ context.Context, s PassSegment) (Text, error) {
		calls++
		if s.RootID != root.ID || len(s.Records) != 0 {
			t.Fatalf("not segment-only: %+v", s)
		}
		return Text{Requests: "none", Topics: "topic", Actions: "none", Values: "42", Omitted: "wording"}, nil
	}
	p := Pass{Policy: Policy{ContextWindow: 1500}, Count: countBytes, Summarize: b.Summarize, Publish: b.Publish}
	fitted, err := p.Fit(t.Context(), v, false)
	if err != nil {
		t.Fatal(err)
	}
	n, err := b.Store.Node(t.Context(), fitted.View.RootID)
	if err != nil || n.Level != 1 || calls != 1 {
		t.Fatalf("node=%+v calls=%d err=%v", n, calls, err)
	}
}

func TestRetractionRebuildsThreeLevelsFromRemainingEvidence(t *testing.T) {
	t.Parallel()
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{"leaf"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("parent")})
	publish(t, s, Proposal{Expected: frontier(t, s), ID: "root", Children: []string{"parent"}, Output: output("root")})
	forget(t, db, r)
	var calls []string
	root, err := s.Rebuild(t.Context(), "conversation", func(_ context.Context, segment Segment) (Output, error) {
		calls = append(calls, segment.NodeID)
		for _, id := range segment.Events {
			if id == "e1" {
				t.Fatal("retracted event returned")
			}
		}
		for _, child := range segment.Children {
			if child.Output.Text != "rebuilt "+child.ID {
				t.Fatal("used stale child", child)
			}
		}
		return output("rebuilt " + segment.NodeID), nil
	})
	if err != nil || root.Level != 2 || root.Revision != 2 || !reflect.DeepEqual(calls, []string{"leaf", "parent", "root"}) {
		t.Fatalf("root=%+v calls=%v err=%v", root, calls, err)
	}
	for _, id := range calls {
		n, err := s.Node(t.Context(), id)
		if err != nil || n.Epoch != 1 || n.Revision != 2 {
			t.Fatalf("node=%+v err=%v", n, err)
		}
	}
}
