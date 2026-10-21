// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func streamFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func streamFrame(t *testing.T, envelope protocol.Envelope) state.Frame {
	t.Helper()
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return state.Frame{Data: data}
}

func TestShellAppliesGeneratedStreamSnapshotAndReplay(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	frame := state.Frame{Data: streamFixture(t, "normal/snapshot")}
	m.Update(shellData{Generation: 1, Message: frame})
	if len(m.content.Blocks) != 1 || m.content.Blocks[0].Text != "complete" || !m.content.Blocks[0].Done || m.content.Topics["conversation:stream-fixture"].Cursor != 2 {
		t.Fatalf("snapshot not projected: %+v", m.content)
	}
	before := m.content
	m.Update(shellData{Generation: 1, Message: frame})
	if !reflect.DeepEqual(before, m.content) {
		t.Fatal("snapshot replay changed state")
	}
	var next protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "streams/item_done"), &next); err != nil {
		t.Fatal(err)
	}
	p := next.Payload.(protocol.ItemDone)
	p.Stream.Key = "second"
	p.Stream.Cursor.Seq = "3"
	p.Stream.Revision = "3"
	text := "\x1b]52;c;secret\a한글"
	p.Text = &text
	next.Payload = p
	m.Update(shellData{Generation: 1, Message: streamFrame(t, next)})
	if len(m.content.Blocks) != 2 || m.content.Blocks[1].Text != "한글" {
		t.Fatal("stream item missing or unsanitized")
	}
	before = m.content
	m.Update(shellData{Generation: 1, Message: streamFrame(t, next)})
	if !reflect.DeepEqual(before, m.content) {
		t.Fatal("stream replay changed state")
	}
}

func TestShellResyncKeepsWindowUntilFreshSnapshot(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/snapshot")}})
	before := m.content
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/resync")}})
	if m.status.State != Resyncing || !reflect.DeepEqual(m.content, before) {
		t.Fatal("resync lost history or status")
	}
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/snapshot")}})
	if m.status.State != Live || !reflect.DeepEqual(m.content, before) {
		t.Fatal("snapshot did not complete resync")
	}
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/resync")}})
	if m.status.State != Live {
		t.Fatal("replayed resync changed display")
	}
}

func TestProjectionRejectsMismatchedEpochAndPreservesFullUint64(t *testing.T) {
	var envelope protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &envelope); err != nil {
		t.Fatal(err)
	}
	p := envelope.Payload.(protocol.Snapshot)
	p.Cursor.Epoch = "43"
	envelope.Payload = p
	if _, err := projectStream(envelope); err == nil {
		t.Fatal("mismatched stream epoch admitted")
	}
	p.Cursor.Epoch = envelope.Epoch
	p.Cursor.Seq = "18446744073709551615"
	p.Items[0].Revision = "18446744073709551615"
	envelope.Payload = p
	projection, err := projectStream(envelope)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := projection.message.(state.Snapshot)
	if !ok || snapshot.Cursor != ^uint64(0) || snapshot.Items[0].Revision != ^uint64(0) {
		t.Fatal("cursor or revision lost precision")
	}
	p.Cursor.Seq = "2e0"
	p.Items[0].Revision = "2.0"
	envelope.Payload = p
	projection, err = projectStream(envelope)
	if err != nil || projection.message.(state.Snapshot).Cursor != 2 {
		t.Fatal("exact JSON integer rejected")
	}
}

func TestShellStreamRevocationClearsCache(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	var envelope protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &envelope); err != nil {
		t.Fatal(err)
	}
	m.Update(shellData{Generation: 1, Message: streamFrame(t, envelope)})
	p := envelope.Payload.(protocol.Snapshot)
	p.RevocationEpoch = "1"
	p.Items = []protocol.StreamItem{}
	envelope.Payload = p
	m.Update(shellData{Generation: 1, Message: streamFrame(t, envelope)})
	if len(m.content.Blocks) != 0 || len(m.blocks) != 0 {
		t.Fatal("revoked history retained")
	}
}

func TestShellOverflowRequestsOnlyAffectedTopic(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/snapshot")}})
	_, cmd := m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/resync_required")}})
	if cmd == nil {
		t.Fatal("overflow did not request a snapshot")
	}
	request, ok := cmd().(shellSubscription)
	if !ok || request.Generation != 1 || request.Request.Topic == nil || *request.Request.Topic != "conversation:stream-fixture" || request.Request.After != nil {
		t.Fatalf("overflow request: %+v", request)
	}
	_, cmd = m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/resync_required")}})
	if cmd != nil {
		t.Fatal("duplicate overflow requested a second snapshot")
	}
}

func TestShellGapCannotBeHealedByOlderEpochSnapshot(t *testing.T) {
	m := newShell(Options{Width: 80}, time.Now)
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/snapshot")}})
	var next protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "streams/output_delta"), &next); err != nil {
		t.Fatal(err)
	}
	p := next.Payload.(protocol.OutputDelta)
	p.Stream.Cursor.Epoch = "43"
	p.Stream.Cursor.Seq = "5"
	next.Epoch = "43"
	next.Payload = p
	_, cmd := m.Update(shellData{Generation: 1, Message: streamFrame(t, next)})
	if cmd == nil || m.status.State != Resyncing {
		t.Fatal("new epoch did not request resync")
	}
	var effects []tea.Msg
	var run func(tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, cmd := range batch {
				run(cmd)
			}
		} else {
			effects = append(effects, msg)
		}
	}
	run(cmd)
	if len(effects) != 1 {
		t.Fatalf("resync effects = %d", len(effects))
	}
	_, cmd = m.Update(effects[0])
	if cmd == nil {
		t.Fatal("resync command not routed to subscription")
	}
	if _, ok := cmd().(shellSubscription); !ok {
		t.Fatal("wrong resync request")
	}
	m.Update(shellData{Generation: 1, Message: state.Frame{Data: streamFixture(t, "normal/snapshot")}})
	if m.status.State != Resyncing {
		t.Fatal("older epoch snapshot healed a newer gap")
	}
	var snapshot protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &snapshot); err != nil {
		t.Fatal(err)
	}
	s := snapshot.Payload.(protocol.Snapshot)
	s.Cursor.Epoch = "43"
	s.Cursor.Seq = "5"
	snapshot.Epoch = "43"
	snapshot.Payload = s
	m.Update(shellData{Generation: 1, Message: streamFrame(t, snapshot)})
	if m.status.State != Live || m.content.Epoch != "43" {
		t.Fatal("fresh snapshot did not restore live")
	}
}
