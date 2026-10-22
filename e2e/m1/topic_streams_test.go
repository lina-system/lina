// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func startTopicsProtocol(t *testing.T, h *harness, hub *clientsrv.Topics, epoch string) *client {
	t.Helper()
	listener, err := clientsrv.Listen(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	capability := protocol.Capability{Name: "conversation", Version: "1.0"}
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{capability}
	}
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(ctx, listener, clientsrv.Identity{
			MainID: "main-e2e", Epoch: epoch, Supported: rows,
			Topics: hub, TopicCapabilities: map[string]protocol.Capability{"conversation:": capability, "core": capability},
			// The same subscription path must remain available during recovery.
			Recovering: func() bool { return true },
		})
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(operationTimeout):
			t.Error("topic listener did not stop")
		}
	})
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{capability}
	answer := c.call(t, protocol.KindHello, hello)
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || answer.Kind != protocol.KindWelcome || !reflect.DeepEqual(welcome.Capabilities, hello.Capabilities) || welcome.Epoch != epoch {
		t.Fatalf("stream welcome: %#v", answer)
	}
	c.welcome = welcome
	return c
}

type topicScenario struct {
	Topic        string                 `json:"topic"`
	OtherTopic   string                 `json:"other_topic"`
	Epoch        string                 `json:"epoch"`
	RingSize     int                    `json:"ring_size"`
	SnapshotSize int                    `json:"snapshot_size"`
	QueueSize    int                    `json:"queue_size"`
	History      []clientsrv.TopicEvent `json:"history"`
	GapAfter     rpc.Cursor             `json:"gap_after"`
	ResumeAfter  rpc.Cursor             `json:"resume_after"`
	SnapshotKeys []string               `json:"snapshot_keys"`
	Overflow     []clientsrv.TopicEvent `json:"overflow"`
}

func wireCursor(c rpc.Cursor) *protocol.StreamCursor {
	return &protocol.StreamCursor{Epoch: c.Epoch, Seq: json.Number(strconv.FormatUint(c.Seq, 10))}
}

func streamNumber(t *testing.T, n json.Number) uint64 {
	t.Helper()
	value, err := strconv.ParseUint(string(n), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func snapshotMessage(t *testing.T, snapshot protocol.Snapshot) state.Snapshot {
	t.Helper()
	items := make([]state.Block, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item.Payload, &body); err != nil {
			t.Fatal(err)
		}
		items = append(items, state.Block{ID: item.Key, Revision: streamNumber(t, item.Revision), Kind: item.Kind, Text: body.Text, Done: item.Kind == protocol.KindItemDone})
	}
	return state.Snapshot{Epoch: snapshot.Cursor.Epoch, Revocation: snapshot.RevocationEpoch, Topic: snapshot.Topic, Cursor: streamNumber(t, snapshot.Cursor.Seq), Items: items}
}

func (c *client) notification(t *testing.T) protocol.Envelope {
	t.Helper()
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		JSONRPC string            `json:"jsonrpc"`
		Method  protocol.Kind     `json:"method"`
		Params  protocol.Envelope `json:"params"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatal(err)
	}
	envelope := frame.Params
	if frame.JSONRPC != "2.0" || frame.Method != envelope.Kind || envelope.ReplyTo != nil || envelope.Sender.Party != protocol.PartyCore || envelope.Sender.ID != c.welcome.MainID || envelope.Epoch != c.welcome.Epoch {
		t.Fatalf("invalid stream notification: %s", line)
	}
	return envelope
}

func TestM1TopicStreams(t *testing.T) {
	data, err := os.ReadFile("topic-streams.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario topicScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	hub, err := clientsrv.NewTopics(scenario.Epoch, clientsrv.TopicOptions{RingSize: scenario.RingSize, SnapshotSize: scenario.SnapshotSize, QueueSize: scenario.QueueSize})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	if err := hub.PublishBatch(scenario.Topic, scenario.History); err != nil {
		t.Fatal(err)
	}
	c := startTopicsProtocol(t, h, hub, scenario.Epoch)

	// A cursor outside the retained ring receives a correlated resync followed
	// by an authoritative snapshot through the real Core client socket.
	answer := c.call(t, protocol.KindSubscribe, protocol.Subscribe{Topic: &scenario.Topic, After: wireCursor(scenario.GapAfter)})
	if answer.Kind != protocol.KindResync {
		t.Fatalf("gap reply: %s", answer.Kind)
	}
	notice := c.notification(t)
	snapshot, ok := notice.Payload.(protocol.Snapshot)
	if !ok || notice.Kind != protocol.KindSnapshot || snapshot.Topic != scenario.Topic || snapshot.Cursor.Epoch != scenario.Epoch || streamNumber(t, snapshot.Cursor.Seq) != uint64(len(scenario.History)) {
		t.Fatalf("snapshot: %#v", notice)
	}
	var keys []string
	for _, item := range snapshot.Items {
		keys = append(keys, item.Key)
	}
	if !reflect.DeepEqual(keys, scenario.SnapshotKeys) {
		t.Fatalf("window: %v", keys)
	}
	message := snapshotMessage(t, snapshot)
	display, _ := state.Reduce(state.State{}, message)
	repeated, _ := state.Reduce(display, message)
	if !reflect.DeepEqual(display, repeated) {
		t.Fatal("repeated snapshot changed display")
	}

	// Resume twice from the same retained cursor. The reducer sees real decoded
	// frames, and repeated message ids/cursors do not duplicate completed items.
	display, _ = state.Reduce(state.State{}, state.Snapshot{Epoch: scenario.Epoch, Revocation: snapshot.RevocationEpoch, Topic: scenario.Topic, Cursor: scenario.ResumeAfter.Seq})
	var firstPass state.State
	var messageIDs []string
	for pass := 0; pass < 2; pass++ {
		answer = c.call(t, protocol.KindSubscribe, protocol.Subscribe{Topic: &scenario.Topic, After: wireCursor(scenario.ResumeAfter)})
		if answer.Kind != protocol.KindSubscribe {
			t.Fatalf("replay acknowledgement: %s", answer.Kind)
		}
		for i := 0; i < len(scenario.SnapshotKeys); i++ {
			event := c.notification(t)
			done, ok := event.Payload.(protocol.ItemDone)
			if !ok || event.Kind != protocol.KindItemDone || done.Stream == nil || done.Text == nil {
				t.Fatalf("replay: %#v", event)
			}
			stream := done.Stream
			if stream.Topic != scenario.Topic || stream.Cursor.Epoch != scenario.Epoch {
				t.Fatal("wrong replay topic or epoch")
			}
			if pass == 0 {
				messageIDs = append(messageIDs, event.MessageID)
			} else if messageIDs[i] != event.MessageID {
				t.Fatal("replay changed a logical message id")
			}
			var commands []state.Cmd
			display, commands = state.Reduce(display, state.ItemDone{Epoch: stream.Cursor.Epoch, Topic: stream.Topic, Cursor: streamNumber(t, stream.Cursor.Seq), Block: state.Block{ID: stream.Key, Revision: streamNumber(t, stream.Revision), Kind: protocol.KindItemDone, Text: *done.Text, Done: true}})
			if len(commands) != 0 {
				t.Fatal("gap in retained replay")
			}
		}
		if pass == 0 {
			firstPass = display
		} else if !reflect.DeepEqual(firstPass, display) {
			t.Fatal("second replay changed display")
		}
	}

	// The atomic committed group is larger than this topic's queue. Its overflow
	// is deterministic even with a fast socket writer; no elapsed-time assertion
	// decides success. Another topic stays live on the same connection.
	answer = c.call(t, protocol.KindSubscribe, protocol.Subscribe{Topic: &scenario.OtherTopic})
	if answer.Kind != protocol.KindSnapshot {
		t.Fatalf("other topic: %s", answer.Kind)
	}
	if err := hub.PublishBatch(scenario.Topic, scenario.Overflow); err != nil {
		t.Fatal(err)
	}
	if err := hub.Publish(scenario.OtherTopic, clientsrv.TopicEvent{Kind: protocol.KindTurnState, Key: "state", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	overflowSeen, otherSeen := false, false
	for range 2 {
		event := c.notification(t)
		switch payload := event.Payload.(type) {
		case protocol.ResyncRequired:
			if payload.Topic != scenario.Topic || overflowSeen {
				t.Fatal("wrong or repeated overflow")
			}
			overflowSeen = true
		case protocol.TurnState:
			if payload.Stream == nil || payload.Stream.Topic != scenario.OtherTopic || streamNumber(t, payload.Stream.Cursor.Seq) != 1 || otherSeen {
				t.Fatal("other topic did not stay live")
			}
			otherSeen = true
		default:
			t.Fatalf("unexpected overflow-period frame: %s", event.Kind)
		}
	}
	if !overflowSeen || !otherSeen {
		t.Fatal("topic overflow closed the connection")
	}
	answer = c.call(t, protocol.KindSubscribe, protocol.Subscribe{Topic: &scenario.Topic})
	recovered, ok := answer.Payload.(protocol.Snapshot)
	if !ok || answer.Kind != protocol.KindSnapshot || streamNumber(t, recovered.Cursor.Seq) != uint64(len(scenario.History)+len(scenario.Overflow)) {
		t.Fatal("overflowed topic could not resubscribe")
	}
	for _, item := range recovered.Items {
		if item.Kind != protocol.KindQuestion {
			t.Fatal("protected question absent from recovered window")
		}
	}
}
