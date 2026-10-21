// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func (identity Identity) topicCapability(topic string) (protocol.Capability, bool) {
	if !validTopic(topic) || identity.Topics == nil || identity.Topics.epoch != identity.Epoch {
		return protocol.Capability{}, false
	}
	if capability, ok := identity.TopicCapabilities[topic]; ok {
		return capability, true
	}
	for _, prefix := range []string{"conversation:", "results:"} {
		if strings.HasPrefix(topic, prefix) {
			capability, ok := identity.TopicCapabilities[prefix]
			return capability, ok
		}
	}
	return protocol.Capability{}, false
}

// topicSession owns the subscriptions of one welcomed connection. The listener
// supplies its negotiated authorization and bounded, cancellation-aware writer.
// A nil RPC id is a notification. The listener closes the transport before
// Close, so a blocked physical write cannot hold shutdown open.
type topicSession struct {
	ctx           context.Context
	cancel        context.CancelFunc
	identity      Identity
	hub           *Topics
	write         func(json.RawMessage, protocol.Envelope) error
	allowed       func(string) bool
	abort         func()
	writeMu       sync.Mutex
	mu            sync.Mutex
	subscriptions map[string]*Subscription
	closed        bool
	workers       sync.WaitGroup
}

func newTopicSession(ctx context.Context, identity Identity, hub *Topics,
	write func(json.RawMessage, protocol.Envelope) error, allowed func(string) bool, abort func()) *topicSession {
	ctx, cancel := context.WithCancel(ctx)
	return &topicSession{ctx: ctx, cancel: cancel, identity: identity, hub: hub, write: write, allowed: allowed, abort: abort, subscriptions: make(map[string]*Subscription)}
}

func (s *topicSession) send(id json.RawMessage, envelope protocol.Envelope, topic string, sub *Subscription) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	current := !s.closed && (sub == nil || s.subscriptions[topic] == sub)
	s.mu.Unlock()
	if !current {
		return s.ctx.Err()
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.write(id, envelope)
}

func (s *topicSession) refuse(id json.RawMessage, request protocol.Envelope, code protocol.ErrorCode) error {
	reply := topicNotification(s.identity, protocol.KindError, protocol.ErrorPayload{})
	reply.ReplyTo = &request.MessageID
	reply.EffectState = protocol.EffectRefused
	reply.Error = &protocol.Error{Code: code, Detail: "topic request refused"}
	return s.send(id, reply, "", nil)
}

func (s *topicSession) remove(topic string, sub *Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subscriptions[topic] == sub {
		delete(s.subscriptions, topic)
	}
	sub.Close()
}

func (h *Topics) cursor(topic string) rpc.Cursor {
	h.mu.Lock()
	defer h.mu.Unlock()
	cursor := rpc.Cursor{Epoch: h.epoch}
	if t := h.topics[topic]; t != nil {
		cursor.Seq = t.seq
	}
	return cursor
}

// handle preserves the pre-existing empty subscribe shape for the listener's
// ordinary dispatcher. Only the additive topic subscription enters this path.
func (s *topicSession) handle(id json.RawMessage, request protocol.Envelope) (bool, error) {
	subscribe, ok := request.Payload.(protocol.Subscribe)
	if request.Kind != protocol.KindSubscribe || !ok || subscribe.Topic == nil {
		return false, nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return true, context.Canceled
	}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	topic := *subscribe.Topic
	if !validTopic(topic) || (subscribe.Active != nil && !*subscribe.Active && subscribe.After != nil) {
		return true, s.refuse(id, request, protocol.ErrorMalformed)
	}
	if s.allowed == nil || !s.allowed(topic) {
		return true, s.refuse(id, request, protocol.ErrorCapabilityNotNegotiated)
	}
	if s.hub == nil {
		return true, s.refuse(id, request, protocol.ErrorUnavailable)
	}
	var after *rpc.Cursor
	if subscribe.After != nil {
		cursor, err := topicReadCursor(*subscribe.After)
		if err != nil {
			return true, s.refuse(id, request, protocol.ErrorMalformed)
		}
		after = &cursor
	}
	active := subscribe.Active == nil || *subscribe.Active
	if !active {
		s.mu.Lock()
		if previous := s.subscriptions[topic]; previous != nil {
			previous.Close()
			delete(s.subscriptions, topic)
		}
		s.mu.Unlock()
		checkpoint := topicWireCursor(s.hub.cursor(topic))
		reply := topicNotification(s.identity, protocol.KindSubscribe, protocol.Subscribe{Topic: &topic, After: &checkpoint, Active: &active})
		reply.ReplyTo = &request.MessageID
		return true, s.send(id, reply, "", nil)
	}
	start, sub, err := s.hub.Subscribe(topic, after)
	if err != nil {
		return true, s.refuse(id, request, protocol.ErrorUnavailable)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		sub.Close()
		return true, context.Canceled
	}
	if previous := s.subscriptions[topic]; previous != nil {
		previous.Close()
	}
	s.subscriptions[topic] = sub
	s.mu.Unlock()
	running := false
	defer func() {
		if !running {
			s.remove(topic, sub)
		}
	}()
	if start.Snapshot != nil {
		snapshot, err := topicSnapshotEnvelope(s.identity, *start.Snapshot)
		if err != nil {
			return true, s.refuse(id, request, protocol.ErrorUnavailable)
		}
		if start.Resync {
			resync := topicResyncEnvelope(s.identity, topic, start.Snapshot.Cursor)
			resync.ReplyTo = &request.MessageID
			if err := s.send(id, resync, topic, sub); err != nil {
				return true, err
			}
			if err := s.send(nil, snapshot, topic, sub); err != nil {
				return true, err
			}
		} else {
			snapshot.ReplyTo = &request.MessageID
			if err := s.send(id, snapshot, topic, sub); err != nil {
				return true, err
			}
		}
	} else {
		checkpoint := *after
		if len(start.Replay) > 0 {
			checkpoint = start.Replay[len(start.Replay)-1].Cursor
		}
		cursor := topicWireCursor(checkpoint)
		reply := topicNotification(s.identity, protocol.KindSubscribe, protocol.Subscribe{Topic: &topic, After: &cursor, Active: &active})
		reply.ReplyTo = &request.MessageID
		if err := s.send(id, reply, topic, sub); err != nil {
			return true, err
		}
		for _, frame := range start.Replay {
			envelope, err := topicFrameEnvelope(s.identity, frame)
			if err != nil {
				return true, s.send(nil, topicOverflowEnvelope(s.identity, topic, s.hub.cursor(topic)), topic, sub)
			}
			if err := s.send(nil, envelope, topic, sub); err != nil {
				return true, err
			}
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return true, context.Canceled
	}
	s.workers.Add(1)
	running = true
	go s.run(topic, sub)
	s.mu.Unlock()
	return true, nil
}

func (s *topicSession) run(topic string, sub *Subscription) {
	defer s.workers.Done()
	defer s.remove(topic, sub)
	for {
		frame, err := sub.Next(s.ctx)
		if err != nil {
			if errors.Is(err, rpc.ErrResyncRequired) {
				if err := s.send(nil, topicOverflowEnvelope(s.identity, topic, s.hub.cursor(topic)), topic, sub); err != nil {
					s.fail()
				}
			}
			return
		}
		envelope, err := topicFrameEnvelope(s.identity, frame)
		if err != nil {
			if err := s.send(nil, topicOverflowEnvelope(s.identity, topic, s.hub.cursor(topic)), topic, sub); err != nil {
				s.fail()
			}
			return
		}
		if err := s.send(nil, envelope, topic, sub); err != nil {
			s.fail()
			return
		}
	}
}

func (s *topicSession) fail() {
	s.cancel()
	if s.abort != nil {
		s.abort()
	}
}

func (s *topicSession) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for topic, sub := range s.subscriptions {
		sub.Close()
		delete(s.subscriptions, topic)
	}
	s.mu.Unlock()
	s.workers.Wait()
}
