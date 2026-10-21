// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

var (
	ErrTopicClosed       = errors.New("topic closed")
	ErrInvalidTopic      = errors.New("invalid topic")
	ErrInvalidEvent      = errors.New("invalid topic event")
	ErrSequenceExhausted = errors.New("topic sequence exhausted")
)

// TopicOptions bounds retained frames, snapshot rows and each subscriber's live
// queue. Zero values select the C-42 defaults. A topic's replay is returned
// separately from its live queue, so catch-up cannot overflow that queue.
type TopicOptions struct {
	RingSize      int
	SnapshotSize  int
	QueueSize     int
	BatchBytes    int
	BatchInterval time.Duration
}

// TopicEvent is an application-projected row. Key is stable within a topic.
// Data is a JSON string for output_delta, or the application's JSON row for
// other kinds. The hub owns copies; callers never share mutable storage with it.
type TopicEvent struct {
	Kind protocol.Kind   `json:"kind"`
	Key  string          `json:"key"`
	Data json.RawMessage `json:"data"`
}

type TopicFrame struct {
	Topic  string     `json:"topic"`
	Cursor rpc.Cursor `json:"cursor"`
	Event  TopicEvent `json:"event"`
}

type TopicSnapshot struct {
	Topic  string       `json:"topic"`
	Cursor rpc.Cursor   `json:"cursor"`
	Items  []TopicEvent `json:"items"`
}

// TopicStart is delivered before reading Subscription.Next. A missing or stale
// cursor gets a snapshot; a gap also requires a resync notice before it.
type TopicStart struct {
	Resync   bool
	Snapshot *TopicSnapshot
	Replay   []TopicFrame
}

type topicState struct {
	seq      uint64
	ring     []TopicFrame
	ringNext int
	items    []TopicEvent
	pending  *TopicEvent
	timer    *time.Timer
	subs     map[*Subscription]struct{}
}

// Topics serializes publication, snapshot capture and subscription attachment.
// No publisher waits for a client. The owner closes it on Core shutdown.
type Topics struct {
	mu      sync.Mutex
	epoch   string
	options TopicOptions
	topics  map[string]*topicState
	closed  bool
}

func NewTopics(epoch string, options TopicOptions) (*Topics, error) {
	if err := (rpc.Cursor{Epoch: epoch}).Validate(); err != nil {
		return nil, err
	}
	if options.RingSize < 0 || options.SnapshotSize < 0 || options.QueueSize < 0 || options.BatchBytes < 0 || options.BatchInterval < 0 {
		return nil, ErrInvalidEvent
	}
	if options.RingSize == 0 {
		options.RingSize = 1024
	}
	if options.SnapshotSize == 0 {
		options.SnapshotSize = 200
	}
	if options.QueueSize == 0 {
		options.QueueSize = 256
	}
	if options.BatchBytes == 0 {
		options.BatchBytes = 4096
	}
	if options.BatchInterval == 0 {
		options.BatchInterval = 16 * time.Millisecond
	}
	return &Topics{epoch: epoch, options: options, topics: make(map[string]*topicState)}, nil
}

func validTopic(topic string) bool {
	switch topic {
	case "running", "progress", "waiting", "exchanges", "accounts", "channels", "core":
		return true
	}
	for _, prefix := range []string{"conversation:", "results:"} {
		if strings.HasPrefix(topic, prefix) && len(topic) > len(prefix) {
			return true
		}
	}
	return false
}

func (h *Topics) topic(name string) *topicState {
	t := h.topics[name]
	if t == nil {
		t = &topicState{subs: make(map[*Subscription]struct{})}
		h.topics[name] = t
	}
	return t
}

func cloneEvent(e TopicEvent) TopicEvent { e.Data = bytes.Clone(e.Data); return e }
func cloneFrame(f TopicFrame) TopicFrame { f.Event = cloneEvent(f.Event); return f }

// Only these associative operations may coalesce. All other kinds, including
// item_done, approvals and questions, force a pending batch out first.
func associative(kind protocol.Kind) bool {
	// Only the documented associative stream kinds coalesce.
	//exhaustive:ignore
	switch kind {
	case protocol.KindOutputDelta, protocol.KindProgressUpdate, protocol.KindRunningUpdate, protocol.KindWaitingUpdate:
		return true
	default:
		return false
	}
}

func mergeEvents(a, b TopicEvent) (TopicEvent, bool) {
	if a.Key != b.Key || a.Kind != b.Kind || !associative(a.Kind) {
		return TopicEvent{}, false
	}
	if a.Kind == protocol.KindOutputDelta {
		var first, second string
		_ = json.Unmarshal(a.Data, &first)
		_ = json.Unmarshal(b.Data, &second)
		a.Data, _ = json.Marshal(first + second)
		return a, true
	}
	return cloneEvent(b), true
}

func (h *Topics) Publish(name string, event TopicEvent) error {
	if !validTopic(name) {
		return ErrInvalidTopic
	}
	if event.Key == "" || event.Kind == "" || !json.Valid(event.Data) {
		return ErrInvalidEvent
	}
	if event.Kind == protocol.KindOutputDelta {
		var text string
		if len(event.Data) == 0 || event.Data[0] != '"' || json.Unmarshal(event.Data, &text) != nil {
			return ErrInvalidEvent
		}
	}
	event = cloneEvent(event)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrTopicClosed
	}
	t := h.topic(name)
	if t.seq == ^uint64(0) {
		return ErrSequenceExhausted
	}
	if t.pending != nil {
		if combined, ok := mergeEvents(*t.pending, event); ok {
			*t.pending = combined
			if h.batchFull(combined) {
				h.flush(name, t)
			}
			return nil
		}
		h.flush(name, t)
		if t.seq == ^uint64(0) {
			return ErrSequenceExhausted
		}
	}
	if !associative(event.Kind) {
		h.emit(name, t, event)
		return nil
	}
	t.pending = &event
	if h.batchFull(event) {
		h.flush(name, t)
		return nil
	}
	pending := t.pending
	t.timer = time.AfterFunc(h.options.BatchInterval, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.closed && t.pending == pending {
			h.flush(name, t)
		}
	})
	return nil
}

func (h *Topics) batchFull(event TopicEvent) bool {
	if event.Kind != protocol.KindOutputDelta {
		return false
	}
	var text string
	_ = json.Unmarshal(event.Data, &text)
	return len(text) >= h.options.BatchBytes
}

// Flush establishes a batch boundary without relying on wall time. Subscribe
// also flushes under the same lock before capturing its snapshot and cursor.
func (h *Topics) Flush(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.topics[name]; t != nil && !h.closed {
		h.flush(name, t)
	}
}

func (h *Topics) flush(name string, t *topicState) {
	if t.pending == nil {
		return
	}
	event := *t.pending
	t.pending = nil
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	h.emit(name, t, event)
}

func (h *Topics) emit(name string, t *topicState, event TopicEvent) {
	t.seq++
	frame := TopicFrame{Topic: name, Cursor: rpc.Cursor{Epoch: h.epoch, Seq: t.seq}, Event: event}
	if len(t.ring) < h.options.RingSize {
		t.ring = append(t.ring, frame)
	} else {
		t.ring[t.ringNext] = frame
		t.ringNext = (t.ringNext + 1) % h.options.RingSize
	}
	// The snapshot is a bounded materialized window, not the delta ring.
	index := -1
	for i, old := range t.items {
		if old.Key == event.Key {
			index = i
			break
		}
	}
	if index >= 0 {
		if combined, ok := mergeEvents(t.items[index], event); ok {
			t.items[index] = combined
		} else {
			t.items[index] = event
		}
	} else {
		if len(t.items) == h.options.SnapshotSize {
			copy(t.items, t.items[1:])
			t.items = t.items[:len(t.items)-1]
		}
		t.items = append(t.items, event)
	}
	for sub := range t.subs {
		if len(sub.queue) == h.options.QueueSize {
			sub.closeLocked(rpc.ErrResyncRequired)
			continue
		}
		sub.queue = append(sub.queue, frame)
		sub.notify()
	}
}

func (h *Topics) Subscribe(name string, after *rpc.Cursor) (TopicStart, *Subscription, error) {
	if !validTopic(name) {
		return TopicStart{}, nil, ErrInvalidTopic
	}
	if after != nil {
		if err := after.Validate(); err != nil {
			return TopicStart{}, nil, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return TopicStart{}, nil, ErrTopicClosed
	}
	t := h.topic(name)
	h.flush(name, t)
	start := TopicStart{}
	oldest := t.seq
	if len(t.ring) > 0 {
		oldest = t.ring[t.ringNext].Cursor.Seq - 1
	}
	gap := after != nil && (after.Epoch != h.epoch || after.Seq < oldest || after.Seq > t.seq)
	if after == nil || gap {
		snapshot := &TopicSnapshot{Topic: name, Cursor: rpc.Cursor{Epoch: h.epoch, Seq: t.seq}, Items: make([]TopicEvent, len(t.items))}
		for i, item := range t.items {
			snapshot.Items[i] = cloneEvent(item)
		}
		start.Resync, start.Snapshot = gap, snapshot
	} else {
		for i := range t.ring {
			frame := t.ring[(t.ringNext+i)%len(t.ring)]
			if frame.Cursor.Seq > after.Seq {
				start.Replay = append(start.Replay, cloneFrame(frame))
			}
		}
	}
	sub := &Subscription{hub: h, topic: t, ready: make(chan struct{}, 1)}
	t.subs[sub] = struct{}{}
	return start, sub, nil
}

// Subscription belongs to one connection and one topic. Overflow is retained
// outside the bounded queue, so even a full queue reliably reports its cause.
type Subscription struct {
	hub   *Topics
	topic *topicState
	queue []TopicFrame
	ready chan struct{}
	err   error
}

func (s *Subscription) notify() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}
func (s *Subscription) closeLocked(err error) {
	if s.err != nil {
		return
	}
	s.err, s.queue = err, nil
	delete(s.topic.subs, s)
	s.notify()
}

func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.closeLocked(ErrTopicClosed)
}

// Next is called by one reader. Context cancellation abandons that read; Close
// releases the subscription. Other subscriptions keep their independent queues.
func (s *Subscription) Next(ctx context.Context) (TopicFrame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return TopicFrame{}, err
		}
		s.hub.mu.Lock()
		if s.err != nil {
			err := s.err
			s.hub.mu.Unlock()
			return TopicFrame{}, err
		}
		if len(s.queue) > 0 {
			frame := cloneFrame(s.queue[0])
			s.queue[0] = TopicFrame{}
			s.queue = s.queue[1:]
			s.hub.mu.Unlock()
			return frame, nil
		}
		s.hub.mu.Unlock()
		select {
		case <-ctx.Done():
			return TopicFrame{}, ctx.Err()
		case <-s.ready:
		}
	}
}

func (h *Topics) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for _, t := range h.topics {
		if t.timer != nil {
			t.timer.Stop()
		}
		t.pending = nil
		for sub := range t.subs {
			sub.closeLocked(ErrTopicClosed)
		}
	}
}
