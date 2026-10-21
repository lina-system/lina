// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

type topicReply struct {
	id       json.RawMessage
	envelope protocol.Envelope
}

func sessionReply(t *testing.T, replies <-chan topicReply) topicReply {
	t.Helper()
	select {
	case reply := <-replies:
		return reply
	case <-time.After(time.Second):
		t.Fatal("topic reply did not arrive")
		return topicReply{}
	}
}

func topicRequest(topic string, active *bool) protocol.Envelope {
	return protocol.Envelope{Kind: protocol.KindSubscribe, MessageID: "message", Payload: protocol.Subscribe{Topic: &topic, Active: active}}
}

func TestTopicSessionAuthorizationCorrelationAndUnsubscribe(t *testing.T) {
	hub := topicHub(t, TopicOptions{})
	replies := make(chan topicReply, 8)
	session := newTopicSession(t.Context(), Identity{MainID: "main", Epoch: "10"}, hub,
		func(id json.RawMessage, envelope protocol.Envelope) error {
			replies <- topicReply{id, envelope}
			return nil
		},
		func(topic string) bool { return topic == "core" }, nil)
	t.Cleanup(session.Close)
	request := topicRequest("conversation:private", nil)
	id := json.RawMessage(`"rpc-id"`)
	if handled, err := session.handle(id, request); !handled || err != nil {
		t.Fatalf("handle: %v %v", handled, err)
	}
	answer := sessionReply(t, replies)
	if string(answer.id) != string(id) || answer.envelope.ReplyTo == nil || *answer.envelope.ReplyTo != request.MessageID || answer.envelope.Error == nil || answer.envelope.Error.Code != protocol.ErrorCapabilityNotNegotiated {
		t.Fatal("uncorrelated authorization refusal")
	}
	if hub.topics["conversation:private"] != nil {
		t.Fatal("unauthorized request created a topic")
	}
	request = topicRequest("core", nil)
	if handled, err := session.handle(id, request); !handled || err != nil {
		t.Fatalf("handle: %v %v", handled, err)
	}
	if sessionReply(t, replies).envelope.Kind != protocol.KindSnapshot {
		t.Fatal("fresh subscription did not snapshot")
	}
	inactive := false
	request = topicRequest("core", &inactive)
	if handled, err := session.handle(id, request); !handled || err != nil {
		t.Fatalf("unsubscribe: %v %v", handled, err)
	}
	answer = sessionReply(t, replies)
	result, ok := answer.envelope.Payload.(protocol.Subscribe)
	if !ok || result.Active == nil || *result.Active || answer.envelope.ReplyTo == nil {
		t.Fatal("unsubscribe not acknowledged")
	}
	hub.mu.Lock()
	count := len(hub.topics["core"].subs)
	hub.mu.Unlock()
	if count != 0 {
		t.Fatal("unsubscribe retained a subscriber")
	}
	legacy := protocol.Envelope{Kind: protocol.KindSubscribe, Payload: protocol.Subscribe{}}
	if handled, err := session.handle(id, legacy); handled || err != nil {
		t.Fatal("legacy subscribe shape was intercepted")
	}
}

func TestTopicSessionOverflowLeavesAnotherTopicAndTransportLive(t *testing.T) {
	hub := topicHub(t, TopicOptions{QueueSize: 1})
	replies := make(chan topicReply, 8)
	var aborted atomic.Int32
	session := newTopicSession(t.Context(), Identity{MainID: "main", Epoch: "10"}, hub,
		func(id json.RawMessage, envelope protocol.Envelope) error {
			replies <- topicReply{id, envelope}
			return nil
		},
		func(string) bool { return true }, func() { aborted.Add(1) })
	t.Cleanup(session.Close)
	for _, topic := range []string{"conversation:test", "core"} {
		if _, err := session.handle(json.RawMessage(`"rpc"`), topicRequest(topic, nil)); err != nil {
			t.Fatal(err)
		}
		if sessionReply(t, replies).envelope.Kind != protocol.KindSnapshot {
			t.Fatal("missing initial snapshot")
		}
	}
	if err := hub.PublishBatch("conversation:test", []TopicEvent{
		{Kind: protocol.KindItemDone, Key: "a", Data: []byte(`{}`)},
		{Kind: protocol.KindItemDone, Key: "b", Data: []byte(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	publish(t, hub, "core", TopicEvent{Kind: protocol.KindTurnState, Key: "state", Data: []byte(`{}`)})
	overflow, live := false, false
	for range 2 {
		answer := sessionReply(t, replies)
		if answer.id != nil || answer.envelope.ReplyTo != nil {
			t.Fatal("notification became a response")
		}
		switch payload := answer.envelope.Payload.(type) {
		case protocol.ResyncRequired:
			if payload.Topic != "conversation:test" || overflow {
				t.Fatal("wrong overflow topic")
			}
			overflow = true
		case protocol.TurnState:
			if payload.Stream == nil || payload.Stream.Topic != "core" || live {
				t.Fatal("other topic is not live")
			}
			live = true
		default:
			t.Fatalf("unexpected notification: %s", answer.envelope.Kind)
		}
	}
	if !overflow || !live || aborted.Load() != 0 {
		t.Fatal("overflow aborted transport or another topic")
	}
}

func TestTopicWireFramesKeepLogicalIdentityAndExactCursors(t *testing.T) {
	identity := Identity{MainID: "main", Epoch: "10"}
	frame := TopicFrame{Topic: "conversation:test", Cursor: rpcCursor("10", 2), Event: TopicEvent{Kind: protocol.KindOutputDelta, Key: "item", Revision: 2, Data: []byte(`"hello"`)}}
	first, err := topicFrameEnvelope(identity, frame)
	if err != nil {
		t.Fatal(err)
	}
	second, err := topicFrameEnvelope(identity, frame)
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageID != second.MessageID || first.AttemptID == second.AttemptID {
		t.Fatal("replay identity does not distinguish logical message and attempt")
	}
	payload, ok := first.Payload.(protocol.OutputDelta)
	if !ok || payload.Stream == nil || payload.Text == nil || *payload.Text != "hello" || payload.Stream.Cursor.Epoch != "10" || payload.Stream.Cursor.Seq != "2" {
		t.Fatalf("invalid typed delta: %#v", first)
	}
	for _, value := range []json.Number{"18446744073709551615", "18446744073709551615.0", "1e3"} {
		cursor, err := topicReadCursor(protocol.StreamCursor{Epoch: "10", Seq: value})
		if err != nil {
			t.Fatal(err)
		}
		want := ^uint64(0)
		if value == "1e3" {
			want = 1000
		}
		if cursor.Seq != want {
			t.Fatalf("rounded cursor %s: %d", value, cursor.Seq)
		}
	}
	frame.Cursor.Epoch = "9"
	if _, err := topicFrameEnvelope(identity, frame); err == nil {
		t.Fatal("accepted mismatched envelope/cursor epoch")
	}
}

func rpcCursor(epoch string, seq uint64) rpc.Cursor { return rpc.Cursor{Epoch: epoch, Seq: seq} }

func TestTopicSessionCloseJoinsSubscriptions(t *testing.T) {
	hub := topicHub(t, TopicOptions{})
	session := newTopicSession(context.Background(), Identity{MainID: "main", Epoch: "10"}, hub,
		func(json.RawMessage, protocol.Envelope) error { return nil }, func(string) bool { return true }, nil)
	if _, err := session.handle(json.RawMessage(`1`), topicRequest("core", nil)); err != nil {
		t.Fatal(err)
	}
	session.Close()
	hub.mu.Lock()
	count := len(hub.topics["core"].subs)
	hub.mu.Unlock()
	if count != 0 {
		t.Fatal("session close retained a subscription")
	}
	if handled, err := session.handle(json.RawMessage(`2`), topicRequest("core", nil)); !handled || err == nil {
		t.Fatal("closed session accepted a request")
	}
}

func TestTopicDeliveryRechecksDynamicAuthorization(t *testing.T) {
	hub := topicHub(t, TopicOptions{})
	var allowed atomic.Bool
	allowed.Store(true)
	replies := make(chan topicReply, 8)
	checked := make(chan struct{}, 1)
	var aborted atomic.Bool
	session := newTopicSession(t.Context(), Identity{MainID: "main", Epoch: "10", AuthorizeTopic: func(context.Context, string) bool {
		ok := allowed.Load()
		if !ok {
			select {
			case checked <- struct{}{}:
			default:
			}
		}
		return ok
	}}, hub,
		func(id json.RawMessage, e protocol.Envelope) error { replies <- topicReply{id, e}; return nil }, func(string) bool { return allowed.Load() }, func() { aborted.Store(true) })
	t.Cleanup(session.Close)
	if _, err := session.handle(json.RawMessage(`"rpc"`), topicRequest("conversation:main:agent", nil)); err != nil {
		t.Fatal(err)
	}
	if sessionReply(t, replies).envelope.Kind != protocol.KindSnapshot {
		t.Fatal("no snapshot")
	}
	allowed.Store(false)
	publish(t, hub, "conversation:main:agent", TopicEvent{Kind: protocol.KindItemDone, Key: "late", Data: []byte(`{"text":"revoked"}`)})
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("delivery did not recheck visibility")
	}
	off := false
	if _, err := session.handle(json.RawMessage(`"off"`), topicRequest("conversation:main:agent", &off)); err != nil {
		t.Fatal(err)
	}
	session.Close()
	if aborted.Load() {
		t.Fatal("revoked topic disconnected unrelated conversation")
	}
	reply := sessionReply(t, replies)
	if reply.envelope.Kind != protocol.KindSubscribe || reply.envelope.Error != nil {
		t.Fatal("revoked subscription could not be detached")
	}
	select {
	case <-replies:
		t.Fatal("revoked content reached the writer")
	default:
	}
}
