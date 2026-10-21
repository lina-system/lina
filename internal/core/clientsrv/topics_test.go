// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func topicHub(t *testing.T, options TopicOptions) *Topics {
	t.Helper()
	h, err := NewTopics("10", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func publish(t *testing.T, h *Topics, topic string, event TopicEvent) {
	t.Helper()
	if err := h.Publish(topic, event); err != nil {
		t.Fatal(err)
	}
}

func subscribe(t *testing.T, h *Topics, topic string, after *rpc.Cursor) (TopicStart, *Subscription) {
	t.Helper()
	start, sub, err := h.Subscribe(topic, after)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return start, sub
}

func next(t *testing.T, sub *Subscription) TopicFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	f, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTopicsGapSnapshotAndReplay(t *testing.T) {
	h := topicHub(t, TopicOptions{RingSize: 2, SnapshotSize: 2})
	for _, id := range []string{"a", "b", "c"} {
		publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindItemDone, Key: id, Data: []byte(`{"done":true}`)})
	}
	for _, cursor := range []*rpc.Cursor{nil, {Epoch: "9", Seq: 3}, {Epoch: "10", Seq: 0}, {Epoch: "10", Seq: 4}} {
		start, _ := subscribe(t, h, "conversation:test", cursor)
		if start.Resync != (cursor != nil) || start.Snapshot == nil || start.Snapshot.Cursor != (rpc.Cursor{Epoch: "10", Seq: 3}) {
			t.Fatalf("bad recovery: %#v", start)
		}
		if len(start.Snapshot.Items) != 2 || start.Snapshot.Items[0].Key != "b" || start.Snapshot.Items[1].Key != "c" {
			t.Fatalf("window: %#v", start.Snapshot.Items)
		}
	}
	cursor := rpc.Cursor{Epoch: "10", Seq: 1}
	start, sub := subscribe(t, h, "conversation:test", &cursor)
	if start.Resync || start.Snapshot != nil || len(start.Replay) != 2 {
		t.Fatalf("ring replay: %#v", start)
	}
	var seq rpc.Sequence
	if err := seq.Snapshot(cursor); err != nil {
		t.Fatal(err)
	}
	var applied []string
	for range 2 {
		for _, f := range start.Replay {
			ok, err := seq.Accept(f.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				applied = append(applied, f.Event.Key)
			}
		}
	}
	if !reflect.DeepEqual(applied, []string{"b", "c"}) {
		t.Fatalf("double replay: %v", applied)
	}
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindItemDone, Key: "d", Data: []byte(`{}`)})
	if f := next(t, sub); f.Cursor.Seq != 4 {
		t.Fatal(f.Cursor)
	}
}

func TestOverflowClosesOnlySlowSubscription(t *testing.T) {
	h := topicHub(t, TopicOptions{QueueSize: 1})
	_, slow := subscribe(t, h, "waiting", nil)
	_, fast := subscribe(t, h, "waiting", nil)
	_, other := subscribe(t, h, "core", nil)
	for _, id := range []string{"a", "b"} {
		publish(t, h, "waiting", TopicEvent{Kind: protocol.KindQuestion, Key: id, Data: []byte(`{}`)})
		if f := next(t, fast); f.Event.Key != id {
			t.Fatal(f)
		}
	}
	if _, err := slow.Next(t.Context()); !errors.Is(err, rpc.ErrResyncRequired) {
		t.Fatalf("overflow: %v", err)
	}
	publish(t, h, "core", TopicEvent{Kind: protocol.KindTurnState, Key: "state", Data: []byte(`{}`)})
	if f := next(t, other); f.Cursor.Seq != 1 {
		t.Fatal(f)
	}
	start, _ := subscribe(t, h, "waiting", nil)
	if len(start.Snapshot.Items) != 2 {
		t.Fatal("overflow lost authoritative questions")
	}
}

func TestCoalescingBeforeSequenceAndProtectedBoundaries(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	_, sub := subscribe(t, h, "conversation:test", nil)
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`"hello"`)})
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`" world"`)})
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindItemDone, Key: "a", Data: []byte(`{"text":"hello world"}`)})
	for seq, want := range []string{`"hello world"`, `{"text":"hello world"}`} {
		f := next(t, sub)
		if f.Cursor.Seq != uint64(seq+1) || string(f.Event.Data) != want {
			t.Fatalf("coalesced frame: %#v", f)
		}
	}
	for _, kind := range []protocol.Kind{protocol.KindItemDone, protocol.KindApprovalRequest, protocol.KindQuestion} {
		for range 2 {
			publish(t, h, "conversation:test", TopicEvent{Kind: kind, Key: "protected", Data: []byte(`{}`)})
		}
		for range 2 {
			if next(t, sub).Event.Kind != kind {
				t.Fatal("protected event lost")
			}
		}
	}
}

func TestBatchTimeAndSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := topicHub(t, TopicOptions{BatchBytes: 4})
		_, sub := subscribe(t, h, "conversation:test", nil)
		publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`"a"`)})
		time.Sleep(16 * time.Millisecond)
		synctest.Wait()
		if string(next(t, sub).Event.Data) != `"a"` {
			t.Fatal("timer did not flush")
		}
		publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`"1234"`)})
		if string(next(t, sub).Event.Data) != `"1234"` {
			t.Fatal("size did not flush")
		}
	})
}

func TestReplaceRowsSnapshotAndIsolation(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	_, sub := subscribe(t, h, "progress", nil)
	for _, data := range []string{`{"percent":1}`, `{"percent":2}`} {
		publish(t, h, "progress", TopicEvent{Kind: protocol.KindProgressUpdate, Key: "p", Data: []byte(data)})
	}
	h.Flush("progress")
	f := next(t, sub)
	if f.Cursor.Seq != 1 || string(f.Event.Data) != `{"percent":2}` {
		t.Fatalf("row coalescing: %#v", f)
	}
	f.Event.Data[0] = '!'
	start, _ := subscribe(t, h, "progress", nil)
	if string(start.Snapshot.Items[0].Data) != `{"percent":2}` {
		t.Fatal("consumer mutated retained row")
	}
	start.Snapshot.Items[0].Data[0] = '!'
	replay, _ := subscribe(t, h, "progress", &rpc.Cursor{Epoch: "10", Seq: 0})
	if string(replay.Replay[0].Event.Data) != `{"percent":2}` {
		t.Fatal("snapshot mutated replay")
	}
}

func TestSubscriptionCloseAndInvalidInputs(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	for _, topic := range []string{"", "unknown", "conversation:", "results:"} {
		if _, _, err := h.Subscribe(topic, nil); err == nil {
			t.Fatalf("accepted topic %q", topic)
		}
	}
	if _, _, err := h.Subscribe("core", &rpc.Cursor{Epoch: "01"}); err == nil {
		t.Fatal("accepted invalid cursor")
	}
	if err := h.Publish("core", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`{}`)}); err == nil {
		t.Fatal("accepted non-text delta")
	}
	_, sub := subscribe(t, h, "core", nil)
	sub.Close()
	if _, err := sub.Next(t.Context()); !errors.Is(err, ErrTopicClosed) {
		t.Fatal(err)
	}
	h.Close()
	if _, _, err := h.Subscribe("core", nil); !errors.Is(err, ErrTopicClosed) {
		t.Fatal(err)
	}
}

func TestDefaultRetentionAndAtomicSnapshotBoundary(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	for i := 0; i < 1025; i++ {
		publish(t, h, "running", TopicEvent{Kind: protocol.KindTaskStatus, Key: fmt.Sprint(i), Data: []byte(`{}`)})
	}
	snapshot, sub := subscribe(t, h, "running", nil)
	if len(snapshot.Snapshot.Items) != 200 || snapshot.Snapshot.Items[0].Key != "825" {
		t.Fatal("default snapshot window")
	}
	replay, _ := subscribe(t, h, "running", &rpc.Cursor{Epoch: "10", Seq: 1})
	if replay.Resync || len(replay.Replay) != 1024 {
		t.Fatal("default ring boundary")
	}
	lost, _ := subscribe(t, h, "running", &rpc.Cursor{Epoch: "10", Seq: 0})
	if !lost.Resync || lost.Snapshot == nil {
		t.Fatal("evicted cursor did not resync")
	}
	publish(t, h, "running", TopicEvent{Kind: protocol.KindTaskStatus, Key: "next", Data: []byte(`{}`)})
	if frame := next(t, sub); frame.Cursor.Seq != snapshot.Snapshot.Cursor.Seq+1 {
		t.Fatal("snapshot/live gap")
	}
}

func TestConcurrentPublishAndSnapshotReplay(t *testing.T) {
	h := topicHub(t, TopicOptions{QueueSize: 1024})
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 500; i++ {
			if err := h.Publish("core", TopicEvent{Kind: protocol.KindTurnState, Key: fmt.Sprint(i), Data: []byte(`{}`)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	start, sub := subscribe(t, h, "core", nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	frames := append([]TopicFrame(nil), sub.queue...)
	h.mu.Unlock()
	if uint64(len(frames)) != 500-start.Snapshot.Cursor.Seq {
		t.Fatal("snapshot/live handoff lost a concurrent publication")
	}
	for i := range frames {
		if frames[i].Cursor.Seq != start.Snapshot.Cursor.Seq+uint64(i)+1 {
			t.Fatal("concurrent publication gap")
		}
	}
	latest, _ := subscribe(t, h, "core", &rpc.Cursor{Epoch: "10", Seq: 0})
	if len(latest.Replay) != 500 || latest.Replay[499].Cursor.Seq != 500 {
		t.Fatal("concurrent replay lost a frame")
	}
}

func TestPendingSnapshotInputOwnershipAndCancellation(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	data := []byte(`"a"`)
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: data})
	data[1] = 'z'
	start, sub := subscribe(t, h, "conversation:test", nil)
	if string(start.Snapshot.Items[0].Data) != `"a"` || start.Snapshot.Cursor.Seq != 1 {
		t.Fatal("pending snapshot boundary or input alias")
	}
	publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`"b"`)})
	h.Flush("conversation:test")
	if f := next(t, sub); f.Cursor.Seq != 2 {
		t.Fatal("post-snapshot sequence")
	}
	start, _ = subscribe(t, h, "conversation:test", nil)
	if string(start.Snapshot.Items[0].Data) != `"ab"` {
		t.Fatal("snapshot contains only last delta")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := sub.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	h.Close()
	if _, err := sub.Next(t.Context()); !errors.Is(err, ErrTopicClosed) {
		t.Fatal(err)
	}
}

func TestSequenceExhaustionAndOptions(t *testing.T) {
	for _, options := range []TopicOptions{{RingSize: -1}, {SnapshotSize: -1}, {QueueSize: -1}, {BatchBytes: -1}, {BatchInterval: -1}} {
		if h, err := NewTopics("1", options); err == nil {
			h.Close()
			t.Fatal("accepted negative option")
		}
	}
	if h, err := NewTopics("01", TopicOptions{}); err == nil {
		h.Close()
		t.Fatal("accepted invalid epoch")
	}
	h := topicHub(t, TopicOptions{})
	h.topic("core").seq = ^uint64(0) - 1
	publish(t, h, "core", TopicEvent{Kind: protocol.KindOutputDelta, Key: "a", Data: []byte(`"a"`)})
	if err := h.Publish("core", TopicEvent{Kind: protocol.KindItemDone, Key: "a", Data: []byte(`{}`)}); !errors.Is(err, ErrSequenceExhausted) {
		t.Fatal(err)
	}
	start, _ := subscribe(t, h, "core", nil)
	if start.Snapshot.Cursor.Seq != ^uint64(0) {
		t.Fatal("sequence wrapped")
	}
}

func TestSnapshotKeepsLatestItemRevision(t *testing.T) {
	h := topicHub(t, TopicOptions{})
	for _, part := range []string{`"a"`, `"b"`} {
		publish(t, h, "conversation:test", TopicEvent{Kind: protocol.KindOutputDelta, Key: "item", Data: []byte(part)})
		h.Flush("conversation:test")
	}
	start, _ := subscribe(t, h, "conversation:test", nil)
	item := start.Snapshot.Items[0]
	if item.Revision != 2 || string(item.Data) != `"ab"` {
		t.Fatalf("snapshot lost latest revision: %#v", item)
	}
	if start.Snapshot.RevocationEpoch != "0" {
		t.Fatal("missing initial revocation epoch")
	}
}

func TestSnapshotCarriesProjectionRevocation(t *testing.T) {
	h := topicHub(t, TopicOptions{RevocationEpoch: "9"})
	start, _ := subscribe(t, h, "core", nil)
	if start.Snapshot.RevocationEpoch != "9" {
		t.Fatal("projection revocation lost")
	}
	if invalid, err := NewTopics("10", TopicOptions{RevocationEpoch: "01"}); err == nil {
		invalid.Close()
		t.Fatal("noncanonical revocation accepted")
	}
}

func TestAtomicPublicationGroupOverflowIsDeterministic(t *testing.T) {
	h := topicHub(t, TopicOptions{QueueSize: 2})
	_, hot := subscribe(t, h, "conversation:test", nil)
	_, other := subscribe(t, h, "core", nil)
	events := []TopicEvent{
		{Kind: protocol.KindItemDone, Key: "a", Data: []byte(`{}`)},
		{Kind: protocol.KindItemDone, Key: "b", Data: []byte(`{}`)},
		{Kind: protocol.KindItemDone, Key: "c", Data: []byte(`{}`)},
	}
	if err := h.PublishBatch("conversation:test", events); err != nil {
		t.Fatal(err)
	}
	if _, err := hot.Next(t.Context()); !errors.Is(err, rpc.ErrResyncRequired) {
		t.Fatal(err)
	}
	publish(t, h, "core", TopicEvent{Kind: protocol.KindTurnState, Key: "state", Data: []byte(`{}`)})
	if next(t, other).Cursor.Seq != 1 {
		t.Fatal("other topic closed")
	}
	invalid := append(events, TopicEvent{Kind: protocol.KindItemDone, Key: "invalid", Data: []byte(`bad`)})
	if err := h.PublishBatch("conversation:test", invalid); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal(err)
	}
	start, _ := subscribe(t, h, "conversation:test", nil)
	if start.Snapshot.Cursor.Seq != 3 {
		t.Fatal("invalid group partially published")
	}
}
