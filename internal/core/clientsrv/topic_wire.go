// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func topicWireCursor(cursor rpc.Cursor) protocol.StreamCursor {
	return protocol.StreamCursor{Epoch: cursor.Epoch, Seq: json.Number(strconv.FormatUint(cursor.Seq, 10))}
}

// The envelope codec has already checked the integer's bounds. Preserve exact
// JSON integer values, including exponent notation, without passing through a
// floating-point value or a signed machine integer.
func topicReadCursor(wire protocol.StreamCursor) (rpc.Cursor, error) {
	cursor := rpc.Cursor{Epoch: wire.Epoch}
	if err := cursor.Validate(); err != nil {
		return rpc.Cursor{}, err
	}
	if !json.Valid([]byte(wire.Seq)) {
		return rpc.Cursor{}, rpc.ErrInvalidCursor
	}
	number, ok := new(big.Rat).SetString(string(wire.Seq))
	if !ok || !number.IsInt() || !number.Num().IsUint64() {
		return rpc.Cursor{}, rpc.ErrInvalidCursor
	}
	cursor.Seq = number.Num().Uint64()
	return cursor, nil
}

func topicNotification(identity Identity, kind protocol.Kind, payload protocol.Payload) protocol.Envelope {
	return protocol.Envelope{
		Protocol: protocol.Protocol{Name: "client", Version: "1.1"},
		Kind:     kind, MessageID: rand.Text(), AttemptID: rand.Text(),
		Sender: protocol.Sender{Party: protocol.PartyCore, ID: identity.MainID},
		Epoch:  identity.Epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: payload,
	}
}

// A retained frame is one logical message across subscriptions and delivery
// attempts. The cursor's topic and main identity prevent cross-stream aliases.
func topicMessageID(identity Identity, frame TopicFrame) string {
	key, _ := json.Marshal(struct {
		Main   string
		Topic  string
		Cursor rpc.Cursor
	}{identity.MainID, frame.Topic, frame.Cursor})
	digest := sha256.Sum256(key)
	return "stream-" + hex.EncodeToString(digest[:])
}

// Payloads remain the generated kind's closed object. Only the hub's text
// accumulator needs projecting from a JSON string into output_delta.text.
// Publication metadata is always assigned by Core, never by a producer's body.
func topicBody(event TopicEvent) (json.RawMessage, error) {
	var body map[string]json.RawMessage
	if event.Kind == protocol.KindOutputDelta {
		data := bytes.TrimSpace(event.Data)
		var text string
		if len(data) == 0 || data[0] != '"' {
			return nil, ErrInvalidEvent
		}
		if err := json.Unmarshal(data, &text); err != nil {
			return nil, ErrInvalidEvent
		}
		value, _ := json.Marshal(text)
		body = map[string]json.RawMessage{"text": value}
	} else if err := json.Unmarshal(event.Data, &body); err != nil || body == nil {
		return nil, ErrInvalidEvent
	}
	if event.AgentID != "" {
		body["agent_id"], _ = json.Marshal(event.AgentID)
	}
	delete(body, "stream")
	encoded, err := json.Marshal(body)
	return encoded, err
}

func topicFrameEnvelope(identity Identity, frame TopicFrame) (protocol.Envelope, error) {
	if frame.Cursor.Epoch != identity.Epoch || frame.Cursor.Validate() != nil {
		return protocol.Envelope{}, rpc.ErrInvalidCursor
	}
	body, err := topicBody(frame.Event)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return protocol.Envelope{}, err
	}
	metadata := protocol.StreamMetadata{
		Topic: frame.Topic, Cursor: topicWireCursor(frame.Cursor), Key: frame.Event.Key,
		Revision: json.Number(strconv.FormatUint(frame.Event.Revision, 10)),
	}
	payload["stream"], err = json.Marshal(metadata)
	if err != nil {
		return protocol.Envelope{}, err
	}
	// Decode through the canonical envelope codec to select the generated
	// payload type and validate its original fields plus optional metadata.
	base := topicNotification(identity, protocol.KindOutputDelta, protocol.OutputDelta{})
	base.MessageID = topicMessageID(identity, frame)
	encoded, err := json.Marshal(base)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return protocol.Envelope{}, err
	}
	wire["kind"], err = json.Marshal(frame.Event.Kind)
	if err != nil {
		return protocol.Envelope{}, err
	}
	wire["payload"], err = json.Marshal(payload)
	if err != nil {
		return protocol.Envelope{}, err
	}
	encoded, err = json.Marshal(wire)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return protocol.Envelope{}, err
	}
	return envelope, nil
}

func topicSnapshotEnvelope(identity Identity, snapshot TopicSnapshot) (protocol.Envelope, error) {
	if snapshot.Cursor.Epoch != identity.Epoch || snapshot.Cursor.Validate() != nil {
		return protocol.Envelope{}, rpc.ErrInvalidCursor
	}
	items := make([]protocol.StreamItem, len(snapshot.Items))
	for i, event := range snapshot.Items {
		body, err := topicBody(event)
		if err != nil {
			return protocol.Envelope{}, err
		}
		items[i] = protocol.StreamItem{
			Kind: event.Kind, Key: event.Key,
			Revision: json.Number(strconv.FormatUint(event.Revision, 10)), Payload: body,
		}
	}
	envelope := topicNotification(identity, protocol.KindSnapshot, protocol.Snapshot{
		Topic: snapshot.Topic, Cursor: topicWireCursor(snapshot.Cursor),
		RevocationEpoch: snapshot.RevocationEpoch, Items: items,
	})
	// Validate nested item payloads before a response or notification is sent.
	if _, err := json.Marshal(envelope); err != nil {
		return protocol.Envelope{}, err
	}
	return envelope, nil
}

func topicResyncEnvelope(identity Identity, topic string, cursor rpc.Cursor) protocol.Envelope {
	return topicNotification(identity, protocol.KindResync, protocol.Resync{Topic: topic, Cursor: topicWireCursor(cursor)})
}

func topicOverflowEnvelope(identity Identity, topic string, cursor rpc.Cursor) protocol.Envelope {
	return topicNotification(identity, protocol.KindResyncRequired, protocol.ResyncRequired{Topic: topic, Cursor: topicWireCursor(cursor)})
}
