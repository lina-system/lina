// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func snapshot() Snapshot {
	return Snapshot{Epoch: "9", Revocation: "1", Topic: "conversation", Cursor: 1, Items: []Block{{ID: "a", Revision: 1, Text: "first", Kind: protocol.KindOutputDelta}}}
}
func apply(t *testing.T, s State, m Msg) State {
	t.Helper()
	next, cmds := Reduce(s, m)
	if len(cmds) != 0 {
		t.Fatalf("unexpected effects for %T: %d", m, len(cmds))
	}
	return next
}

func TestReplayAndSnapshot(t *testing.T) {
	s := apply(t, State{}, snapshot())
	original := s
	delta := Delta{Epoch: "9", Topic: "conversation", Cursor: 2, ID: "a", Revision: 2, Text: "\x1b[31m second\x1b[0m"}
	s = apply(t, s, delta)
	if original.Blocks[0].Text != "first" {
		t.Fatal("mutated previous state")
	}
	if s.Blocks[0].Text != "first second" {
		t.Fatal("delta not sanitized/applied")
	}
	for _, m := range []Msg{delta, snapshot()} {
		if got := apply(t, s, m); !reflect.DeepEqual(got, s) {
			t.Fatalf("replay changed state: %T", m)
		}
	}
	done := ItemDone{Epoch: "9", Topic: "conversation", Cursor: 3, Block: Block{ID: "a", Revision: 3, Text: "authoritative", Kind: protocol.KindItemDone}}
	s = apply(t, s, done)
	if len(s.Blocks) != 1 || s.Blocks[0].Text != "authoritative" || !s.Blocks[0].Done {
		t.Fatal("done did not replace delta")
	}
	if got := apply(t, s, done); !reflect.DeepEqual(got, s) {
		t.Fatal("duplicate done")
	}
	snap := snapshot()
	snap.Cursor = 3
	snap.Items = append([]Block(nil), s.Blocks...)
	if got := apply(t, s, snap); !reflect.DeepEqual(got, s) {
		t.Fatal("double snapshot changed state")
	}
}

func TestResyncAndRevocation(t *testing.T) {
	s := apply(t, State{}, snapshot())
	s = apply(t, s, Select{ID: "a"})
	s = apply(t, s, Fold{ID: "a", Closed: true})
	s = apply(t, s, Scroll{ID: "a", Row: 2})
	s = apply(t, s, Notice{ID: "notice", Code: "local", Text: "hi"})
	next, cmds := Reduce(s, Delta{Epoch: "9", Topic: "conversation", Cursor: 3, ID: "a", Revision: 3, Text: "gap"})
	if !next.Topics["conversation"].OutOfSync || len(cmds) != 1 {
		t.Fatal("gap did not request resync")
	}
	if req, ok := cmds[0](context.Background()).(ResyncRequest); !ok || req.Topic != "conversation" {
		t.Fatal("wrong command")
	}
	if again, more := Reduce(next, Delta{Epoch: "9", Topic: "conversation", Cursor: 4, ID: "a", Revision: 4}); len(more) != 0 || !reflect.DeepEqual(again, next) {
		t.Fatal("resync storm")
	}
	snap := snapshot()
	snap.Cursor = 4
	snap.Items[0].Revision = 4
	snap.Items[0].Text = "recovered"
	next = apply(t, next, snap)
	if next.Selected != "a" || !next.Folds["a"] || next.Scroll.ID != "a" || len(next.Notices) != 1 {
		t.Fatal("resync lost local state")
	}
	// Compare epochs numerically, including beyond machine integers.
	snap.Epoch = "10"
	next = apply(t, next, snap)
	if next.Epoch != "10" {
		t.Fatal("lexical epoch comparison")
	}
	snap.Epoch = "18446744073709551616"
	next = apply(t, next, snap)
	snap.Revocation = "2"
	snap.Items = nil
	snap.Cursor = 0
	next = apply(t, next, snap)
	if len(next.Blocks) != 0 || len(next.Folds) != 0 || next.Selected != "" || next.Scroll.ID != "" || len(next.Notices) == 0 {
		t.Fatal("forgotten state survived")
	}
	if old := apply(t, next, snapshot()); !reflect.DeepEqual(old, next) {
		t.Fatal("old epoch resurrected data")
	}
}

func TestSnapshotOwnsItsDataAndTopics(t *testing.T) {
	snap := snapshot()
	s := apply(t, State{}, snap)
	snap.Items[0].Text = "mutated"
	if s.Blocks[0].Text != "first" {
		t.Fatal("snapshot aliases input")
	}
	other := snapshot()
	other.Topic = "other"
	other.Items[0].ID = "b"
	s = apply(t, s, other)
	newer := snapshot()
	newer.Cursor = 2
	newer.Items = nil
	s = apply(t, s, newer)
	if len(s.Blocks) != 1 || s.Blocks[0].ID != "b" {
		t.Fatal("snapshot erased other topic")
	}
}

func TestSendFailureGenerationAndEcho(t *testing.T) {
	s := apply(t, State{}, EditDraft{Draft: Draft{Text: "old", Attachments: []string{"one"}}})
	next, cmds := Reduce(s, Send{Key: "key"})
	if !next.Busy || next.Draft.Text != "" || len(cmds) != 1 || len(next.Blocks) != 1 {
		t.Fatal("send not atomic")
	}
	req := cmds[0](context.Background()).(SendRequest)
	if req.Gen != next.Gen || req.Draft.Text != "old" {
		t.Fatal("wrong send intent")
	}
	next = apply(t, next, EditDraft{Draft: Draft{Text: "new", Attachments: []string{"two"}}})
	stale := apply(t, next, SendResult{Gen: next.Gen + 1, Key: "key", Failed: true})
	if !reflect.DeepEqual(stale, next) {
		t.Fatal("stale result applied")
	}
	failed := apply(t, next, SendResult{Gen: next.Gen, Key: "key", Failed: true})
	if failed.Busy || failed.Draft.Text != "old\nnew" || len(failed.Draft.Attachments) != 2 || len(failed.Blocks) != 0 {
		t.Fatal("failed draft not merged")
	}
	next = apply(t, next, Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation", Cursor: 1, Items: []Block{{ID: "real", EchoKey: "key", Revision: 1, Text: "old", Done: true}}})
	if len(next.Blocks) != 1 || next.Blocks[0].ID != "real" {
		t.Fatal("echo not replaced")
	}
}

func TestClockAndReceiveBoundary(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := snapshot()
	snap.ServerTime = now.Add(2 * time.Second)
	snap.ClientTime = now
	s := apply(t, State{}, snap)
	s = apply(t, s, Tick{Now: now.Add(5 * time.Second)})
	if !s.ServerNow.Equal(now.Add(7 * time.Second)) {
		t.Fatal("wrong server offset")
	}
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/welcome.json")
	if err != nil {
		t.Fatal(err)
	}
	s = apply(t, State{}, Frame{Data: data})
	if s.Epoch != "42" {
		t.Fatal("welcome epoch")
	}
	before := s
	s = apply(t, s, Frame{Data: data})
	if !reflect.DeepEqual(before, s) {
		t.Fatal("replayed welcome changed state")
	}
	s = apply(t, s, Frame{Data: []byte(`{}`)})
	if s.Malformed != 1 || s.Placeholder != protocol.ErrorMalformed || len(s.Blocks) != 0 {
		t.Fatal("invalid frame became content")
	}
}

func TestDeltaSplitEscapeAndUTF8(t *testing.T) {
	s := apply(t, State{}, snapshot())
	input := "한\x1b]52;c;hidden\a글"
	for i := range len(input) {
		s = apply(t, s, Delta{Epoch: "9", Topic: "conversation", Cursor: uint64(i + 2), ID: "a", Revision: uint64(i + 2), Text: input[i : i+1]})
	}
	if s.Blocks[0].Text != "first한글" {
		t.Fatalf("split filtering: %q", s.Blocks[0].Text)
	}
}

func TestSnapshotRejectionAndDuplicateIDs(t *testing.T) {
	s := apply(t, State{}, snapshot())
	s, _ = Reduce(s, Delta{Epoch: "9", Topic: "conversation", Cursor: 3, ID: "a", Revision: 3})
	stale := snapshot()
	stale.Cursor = 0
	stale.Items = nil
	if got := apply(t, s, stale); !reflect.DeepEqual(got, s) {
		t.Fatal("stale resync snapshot rolled back cursor")
	}
	next := snapshot()
	next.Cursor = 4
	next.Items = append(next.Items, Block{ID: "a", Revision: 2, Text: "replacement"})
	s = apply(t, s, next)
	if len(s.Blocks) != 1 || s.Blocks[0].Revision != 2 {
		t.Fatal("snapshot duplicates survived")
	}
	next.Revocation = "2"
	next.Cursor = 0
	next.Items = nil
	s = apply(t, s, next)
	if got := apply(t, s, snapshot()); !reflect.DeepEqual(got, s) {
		t.Fatal("old revocation resurrected data")
	}
}

func TestSnapshotOpenEscapeContinuesInDelta(t *testing.T) {
	snap := snapshot()
	snap.Items[0].Text = "safe\x1b]52;"
	s := apply(t, State{}, snap)
	s = apply(t, s, Delta{Epoch: "9", Topic: "conversation", Cursor: 2, ID: "a", Revision: 2, Text: "c;hidden\aend"})
	if s.Blocks[0].Text != "safeend" {
		t.Fatal("snapshot lost sanitizer continuation")
	}
}

func TestEpochDuringSendRestoresDraft(t *testing.T) {
	s := apply(t, State{}, snapshot())
	s = apply(t, s, EditDraft{Draft: Draft{Text: "unsent"}})
	s, cmds := Reduce(s, Send{Key: "pending"})
	request := cmds[0](context.Background()).(SendRequest)
	s = apply(t, s, EditDraft{Draft: Draft{Text: "new"}})
	snap := snapshot()
	snap.Epoch = "10"
	s = apply(t, s, snap)
	if s.Busy || s.Draft.Text != "unsent\nnew" {
		t.Fatal("epoch left send stuck")
	}
	if got := apply(t, s, SendResult{Gen: request.Gen, Key: request.Key, Failed: true}); !reflect.DeepEqual(s, got) {
		t.Fatal("old send result applied")
	}
}

func TestCommandAndDraftOwnership(t *testing.T) {
	d := Draft{Text: "text", Attachments: []string{"file"}}
	s := apply(t, State{}, EditDraft{Draft: d})
	d.Attachments[0] = "changed"
	if s.Draft.Attachments[0] != "file" {
		t.Fatal("draft aliases input")
	}
	next, cmds := Reduce(s, Send{Key: "key"})
	result := cmds[0](context.Background()).(SendRequest)
	result.Draft.Attachments[0] = "changed"
	again := cmds[0](context.Background()).(SendRequest)
	if again.Draft.Attachments[0] != "file" || s.Draft.Attachments[0] != "file" {
		t.Fatal("command aliases draft")
	}
	next = apply(t, next, SendResult{Gen: next.Gen, Key: "key", Failed: true})
	if next.Draft.Attachments[0] != "file" {
		t.Fatal("command mutated pending send")
	}
}

func TestReceiveUnknownAndErrorStayOutsideConversation(t *testing.T) {
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/output_delta.json")
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(data), `"kind": "output_delta"`, `"kind": "future_kind"`, 1)
	s := apply(t, State{}, Frame{Data: []byte(unknown)})
	if s.Unknown != 1 || s.Malformed != 0 || len(s.Blocks) != 0 {
		t.Fatal("unknown frame admitted")
	}
	data, err = os.ReadFile("../../../protocol/fixtures/client/1.0/unsupported_combination.json")
	if err != nil {
		t.Fatal(err)
	}
	s = apply(t, s, Frame{Data: data})
	if s.Placeholder != protocol.ErrorUnsupportedCombination || len(s.Blocks) != 0 {
		t.Fatal("error became content")
	}
}

func TestRevisionReplayConsumesCursorAndDoneWins(t *testing.T) {
	s := apply(t, State{}, snapshot())
	s = apply(t, s, Delta{Epoch: "9", Topic: "conversation", Cursor: 2, ID: "a", Revision: 1, Text: "duplicate revision"})
	if s.Topics["conversation"].Cursor != 2 || s.Blocks[0].Text != "first" {
		t.Fatal("replayed revision mishandled")
	}
	s = apply(t, s, ItemDone{Epoch: "9", Topic: "conversation", Cursor: 3, Block: Block{ID: "a", Revision: 2, Text: "final"}})
	s = apply(t, s, Delta{Epoch: "9", Topic: "conversation", Cursor: 4, ID: "a", Revision: 3, Text: "late"})
	if s.Blocks[0].Text != "final" || !s.Blocks[0].Done {
		t.Fatal("delta overwrote done")
	}
}
