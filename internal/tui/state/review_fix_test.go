// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"os"
	"reflect"
	"slices"
	"testing"
)

func twoTopics(t *testing.T) State {
	t.Helper()
	s := apply(t, State{}, snapshot())
	other := snapshot()
	other.Topic = "background"
	other.Items[0].ID = "b"
	return apply(t, s, other)
}

func expectResyncs(t *testing.T, s State, cmds []Cmd, topics ...string) {
	t.Helper()
	var got []string
	for _, cmd := range cmds {
		request, ok := cmd(context.Background()).(ResyncRequest)
		if !ok || request.Gen != s.Gen {
			t.Fatalf("resync must carry the new generation %d: %#v", s.Gen, request)
		}
		topic, exists := s.Topics[request.Topic]
		if !exists || !topic.OutOfSync {
			t.Fatalf("resync topic %q is not marked out of sync", request.Topic)
		}
		got = append(got, request.Topic)
	}
	slices.Sort(got)
	slices.Sort(topics)
	if !slices.Equal(got, topics) {
		t.Fatalf("resync topics = %v, want exactly %v", got, topics)
	}
}

func TestNewEpochWelcomeResyncsTopics(t *testing.T) {
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/welcome.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, priorGap := range []bool{false, true} {
		name := "synced"
		if priorGap {
			name = "previous resync pending"
		}
		t.Run(name, func(t *testing.T) {
			before := twoTopics(t)
			if priorGap {
				var cmds []Cmd
				before, cmds = Reduce(before, Delta{Epoch: "9", Topic: "background", Cursor: 3, ID: "b", Revision: 3})
				expectResyncs(t, before, cmds, "background")
			}
			welcome := Frame{Data: data}
			s, cmds := Reduce(before, welcome)
			if s.Epoch != "42" || s.Gen <= before.Gen {
				t.Fatal("welcome did not advance epoch and generation")
			}
			expectResyncs(t, s, cmds, "conversation", "background")
			if before.Topics["conversation"].OutOfSync {
				t.Fatal("welcome mutated previous state")
			}
			if repeated := apply(t, s, welcome); !reflect.DeepEqual(repeated, s) {
				t.Fatal("duplicate welcome changed state")
			}
			for _, topic := range []string{"conversation", "background"} {
				id := "a"
				if topic == "background" {
					id = "b"
				}
				for _, msg := range []Msg{
					Delta{Epoch: "42", Topic: topic, Cursor: 2, ID: id, Revision: 2, Text: "pending"},
					ItemDone{Epoch: "42", Topic: topic, Cursor: 3, Block: Block{ID: id, Revision: 3, Text: "pending"}},
				} {
					if waiting := apply(t, s, msg); !reflect.DeepEqual(waiting, s) {
						t.Fatal("pending resync accepted data or requested another resync")
					}
				}
				fresh := snapshot()
				fresh.Epoch, fresh.Topic, fresh.Items[0].ID = "42", topic, id
				s = apply(t, s, fresh)
				s = apply(t, s, Delta{Epoch: "42", Topic: topic, Cursor: 2, ID: id, Revision: 2, Text: " recovered"})
				if s.Topics[topic].OutOfSync || s.Topics[topic].Cursor != 2 || s.Blocks[s.find(id)].Text != "first recovered" {
					t.Fatalf("topic %q did not recover after snapshot", topic)
				}
			}
		})
	}
}

func TestNewEpochSnapshotResyncsOtherTopics(t *testing.T) {
	for _, revocation := range []string{"1", "2"} {
		t.Run("revocation="+revocation, func(t *testing.T) {
			before := twoTopics(t)
			fresh := snapshot()
			fresh.Epoch, fresh.Revocation = "10", revocation
			s, cmds := Reduce(before, fresh)
			expectResyncs(t, s, cmds, "background")
			if s.Gen <= before.Gen || s.Topics["conversation"].OutOfSync {
				t.Fatal("applied snapshot topic must already be synchronized")
			}
			if repeated := apply(t, s, fresh); !reflect.DeepEqual(repeated, s) {
				t.Fatal("duplicate snapshot changed state")
			}
			delta := Delta{Epoch: "10", Topic: "background", Cursor: 2, ID: "b", Revision: 2}
			if waiting := apply(t, s, delta); !reflect.DeepEqual(waiting, s) {
				t.Fatal("duplicate resync while waiting")
			}
			fresh.Topic, fresh.Items[0].ID = "background", "b"
			s = apply(t, s, fresh)
			s = apply(t, s, delta)
			if s.Topics["background"].OutOfSync || s.Topics["background"].Cursor != 2 {
				t.Fatal("snapshot did not recover other topic")
			}
		})
	}
}

func TestRevocationDuringSendRestoresDraft(t *testing.T) {
	s := apply(t, State{}, snapshot())
	s = apply(t, s, EditDraft{Draft: Draft{Text: "unsent", Attachments: []string{"first"}}})
	s, cmds := Reduce(s, Send{Key: "pending"})
	request := cmds[0](context.Background()).(SendRequest)
	s = apply(t, s, EditDraft{Draft: Draft{Text: "new", Attachments: []string{"second"}}})
	before := s
	fresh := snapshot()
	fresh.Revocation, fresh.Cursor, fresh.Items = "2", 0, nil
	s = apply(t, s, fresh)
	want := Draft{Text: "unsent\nnew", Attachments: []string{"first", "second"}}
	if !reflect.DeepEqual(s.Draft, want) {
		t.Fatalf("restored draft = %#v, want %#v", s.Draft, want)
	}
	if s.Busy || s.pending != nil || len(s.Blocks) != 0 || s.Gen <= request.Gen {
		t.Fatal("revocation did not invalidate pending send and cached items")
	}
	if before.Draft.Text != "new" || before.pending == nil || !before.Busy {
		t.Fatal("revocation mutated previous state")
	}
	if late := apply(t, s, SendResult{Gen: request.Gen, Key: request.Key, Failed: true}); !reflect.DeepEqual(late, s) {
		t.Fatal("late result restored the draft twice")
	}
	if repeated := apply(t, s, fresh); !reflect.DeepEqual(repeated, s) {
		t.Fatal("replayed revocation restored the draft twice")
	}
}

func TestItemDoneAtEqualRevisionSealsPartial(t *testing.T) {
	before := apply(t, State{}, snapshot())
	done := ItemDone{Epoch: "9", Topic: "conversation", Cursor: 2, Block: Block{ID: "a", Revision: 1, Text: "authoritative"}}
	s := apply(t, before, done)
	if len(s.Blocks) != 1 || !s.Blocks[0].Done || s.Blocks[0].Text != "authoritative" || s.Blocks[0].Revision != 1 {
		t.Fatal("equal-revision completion did not seal and replace partial text")
	}
	if before.Blocks[0].Done || before.Blocks[0].Text != "first" {
		t.Fatal("completion mutated previous state")
	}
	if repeated := apply(t, s, done); !reflect.DeepEqual(repeated, s) {
		t.Fatal("completion replay changed state")
	}
	done.Cursor, done.Block.Text = 3, "duplicate"
	s = apply(t, s, done)
	if s.Blocks[0].Text != "authoritative" {
		t.Fatal("equal-revision duplicate overwrote sealed text")
	}
	s = apply(t, s, Delta{Epoch: "9", Topic: "conversation", Cursor: 4, ID: "a", Revision: 2, Text: "late"})
	if !s.Blocks[0].Done || s.Blocks[0].Text != "authoritative" {
		t.Fatal("late delta changed sealed text")
	}
}
