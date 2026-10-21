// SPDX-License-Identifier: Apache-2.0

package state

import (
	"reflect"
	"slices"
	"testing"
)

func TestSnapshotRestoresAuthoritativeOrder(t *testing.T) {
	for _, epoch := range []string{"9", "10"} {
		for _, noticeID := range []string{"live-stop", "stored-stop"} {
			t.Run(epoch+"/"+noticeID, func(t *testing.T) {
				before := State{
					Epoch: "9", Revocation: "1", Topics: map[string]Topic{"conversation": {Cursor: 5}},
					Blocks: []Block{
						{ID: "background", Topic: "other", Text: "keep"},
						{ID: "first", Topic: "conversation", Done: true},
						{ID: "follow-up", Topic: "conversation", Done: true},
						{ID: "reply", Topic: "conversation", Done: true},
						{ID: "live-stop", Topic: "conversation", Done: true},
					},
					Selected: "follow-up", Folds: map[string]bool{"reply": true}, Scroll: Position{ID: "reply", Row: 2},
					Notices: []Notice{{ID: "local", Text: "local notice"}}, Draft: Draft{Text: "draft"},
				}
				snap := Snapshot{Epoch: epoch, Revocation: "1", Topic: "conversation", Cursor: 6, Items: []Block{
					{ID: "first", Done: true}, {ID: "partial", Done: true}, {ID: noticeID, Done: true},
					{ID: "follow-up", Done: true}, {ID: "reply", Done: true},
				}}
				s := apply(t, before, snap)
				var ids []string
				for _, block := range s.Blocks {
					ids = append(ids, block.ID)
				}
				want := []string{"background", "first", "partial", noticeID, "follow-up", "reply"}
				if !slices.Equal(ids, want) {
					t.Fatalf("snapshot order = %v, want %v", ids, want)
				}
				if before.Blocks[2].ID != "follow-up" || len(before.Blocks) != 5 {
					t.Fatal("snapshot mutated its input")
				}
				if s.Blocks[0] != before.Blocks[0] || s.Selected != before.Selected || !s.Folds["reply"] || s.Scroll != before.Scroll || s.Draft.Text != "draft" || s.Notices[0] != before.Notices[0] {
					t.Fatal("ordering changed another topic or local view state")
				}
				if replay := apply(t, s, snap); !reflect.DeepEqual(replay, s) {
					t.Fatal("duplicate snapshot changed state")
				}
			})
		}
	}
}

func TestSnapshotOrderingReconcilesEchoAtItsAnchor(t *testing.T) {
	s := apply(t, State{}, Snapshot{Epoch: "9", Revocation: "1", Topic: "conversation", Cursor: 1, Items: []Block{{ID: "first", Done: true}}})
	s = apply(t, s, EditDraft{Draft: Draft{Text: "sent"}})
	s, _ = Reduce(s, Send{Key: "echo"})
	s = apply(t, s, Select{ID: "echo"})
	s = apply(t, s, Fold{ID: "echo", Closed: true})
	s = apply(t, s, Scroll{ID: "echo", Row: 1})
	s.Blocks = append(s.Blocks, Block{ID: "other", Topic: "background"})
	snap := Snapshot{Epoch: "9", Revocation: "1", Topic: "conversation", Cursor: 2, Items: []Block{
		{ID: "first", Done: true}, {ID: "stopped", Done: true},
		{ID: "sent", EchoKey: "echo", Text: "sent", Done: true},
		{ID: "stopped", Revision: 2, Done: true}, // Deduplicate without moving it.
	}}
	s = apply(t, s, snap)
	var ids []string
	for _, b := range s.Blocks {
		ids = append(ids, b.ID)
	}
	if !slices.Equal(ids, []string{"first", "stopped", "sent", "other"}) || s.Busy || s.pending != nil {
		t.Fatalf("echo reconciliation/order: %v; busy=%t", ids, s.Busy)
	}
	if s.Selected != "sent" || !s.Folds["sent"] || s.Folds["echo"] || s.Scroll.ID != "sent" || s.Scroll.Row != 1 || s.Blocks[1].Revision != 2 {
		t.Fatal("echo lost its view identity or snapshot kept an older revision")
	}
}
