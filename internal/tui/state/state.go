// SPDX-License-Identifier: Apache-2.0

// Package state owns the pure, Charm-free display reducer. Stream messages are
// internal projections, not wire payloads; Frame admits generated wire types.
package state

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

type Msg interface{ isMsg() }
type Cmd func(context.Context) Msg

type Block struct {
	ID, Topic, EchoKey, Text string
	Revision                 uint64
	Kind                     protocol.Kind
	Done, Local              bool
	filter                   sanitize.Stream
}
type Topic struct {
	Cursor    uint64
	OutOfSync bool
}
type Draft struct {
	Text        string
	Attachments []string
}
type Position struct {
	ID  string
	Row int
}
type State struct {
	Epoch, Revocation  string
	Topics             map[string]Topic
	Blocks             []Block
	Selected           string
	Folds              map[string]bool
	Scroll             Position
	Notices            []Notice
	Draft              Draft
	Busy, Stopping     bool
	Gen                uint64
	pending            *SendRequest
	Offset             time.Duration
	ServerNow          time.Time
	Malformed, Unknown uint64
	Placeholder        protocol.ErrorCode
	Capabilities       []protocol.Capability
	Agents             []protocol.Agent
}

type Snapshot struct {
	Epoch, Revocation, Topic string
	Cursor                   uint64
	Items                    []Block
	ServerTime, ClientTime   time.Time
}
type Delta struct {
	Epoch, Topic, ID, Text string
	Cursor, Revision       uint64
}
type ItemDone struct {
	Epoch, Topic string
	Cursor       uint64
	Block        Block
}
type Frame struct{ Data []byte }
type Select struct{ ID string }
type Fold struct {
	ID     string
	Closed bool
}
type Scroll struct {
	ID  string
	Row int
}
type Notice struct{ ID, Code, Text string }
type EditDraft struct{ Draft Draft }
type Send struct{ Key string }
type SendRequest struct {
	Gen   uint64
	Key   string
	Draft Draft
}
type SendResult struct {
	Gen    uint64
	Key    string
	Failed bool
}
type ResyncRequest struct {
	Gen   uint64
	Topic string
}
type Interrupt struct{}
type InterruptRequest struct{ Gen uint64 }
type Tick struct{ Now time.Time }

func (Snapshot) isMsg()         {}
func (Delta) isMsg()            {}
func (ItemDone) isMsg()         {}
func (Frame) isMsg()            {}
func (Select) isMsg()           {}
func (Fold) isMsg()             {}
func (Scroll) isMsg()           {}
func (Notice) isMsg()           {}
func (EditDraft) isMsg()        {}
func (Send) isMsg()             {}
func (SendRequest) isMsg()      {}
func (SendResult) isMsg()       {}
func (ResyncRequest) isMsg()    {}
func (Interrupt) isMsg()        {}
func (InterruptRequest) isMsg() {}
func (Tick) isMsg()             {}

func cloneDraft(d Draft) Draft { d.Attachments = slices.Clone(d.Attachments); return d }
func (s State) clone() State {
	s.Blocks = slices.Clone(s.Blocks)
	s.Topics = maps.Clone(s.Topics)
	s.Folds = maps.Clone(s.Folds)
	s.Notices = slices.Clone(s.Notices)
	return s
}

// Compare decimal epochs without machine-integer limits or lexical ordering.
func compare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func epoch(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func (s *State) notice(n Notice) {
	for _, old := range s.Notices {
		if old.ID == n.ID {
			return
		}
	}
	n.Text = sanitize.Display(n.Text)
	s.Notices = append(s.Notices, n)
}
func (s *State) setEpoch(e string) {
	if e == s.Epoch {
		return
	}
	if s.Epoch != "" {
		s.notice(Notice{ID: "epoch:" + e, Code: "epoch_changed"})
	}
	s.Epoch = e
	if s.pending != nil {
		s.restoreDraft()
	}
	s.Stopping = false
	s.Gen++
	for name := range s.Topics {
		s.Topics[name] = Topic{OutOfSync: true}
	}
}
func (s *State) resync(topic string) []Cmd {
	t := s.Topics[topic]
	if t.OutOfSync {
		return nil
	}
	if s.Topics == nil {
		s.Topics = map[string]Topic{}
	}
	t.OutOfSync = true
	s.Topics[topic] = t
	req := ResyncRequest{Gen: s.Gen, Topic: topic}
	return []Cmd{func(context.Context) Msg { return req }}
}
func (s State) find(id string) int {
	return slices.IndexFunc(s.Blocks, func(b Block) bool { return b.ID == id })
}
func (s *State) remapEcho(b Block) {
	if !b.Local && s.pending != nil && b.EchoKey == s.pending.Key {
		s.pending = nil
		s.Busy = false
	}
	if b.EchoKey == "" {
		return
	}
	if s.Selected == b.EchoKey {
		s.Selected = b.ID
	}
	if s.Scroll.ID == b.EchoKey {
		s.Scroll.ID = b.ID
	}
	if closed, ok := s.Folds[b.EchoKey]; ok {
		delete(s.Folds, b.EchoKey)
		s.Folds[b.ID] = closed
	}
}
func (s *State) put(b Block) {
	at := s.find(b.ID)
	if at < 0 && b.EchoKey != "" {
		at = slices.IndexFunc(s.Blocks, func(old Block) bool { return old.Local && old.EchoKey == b.EchoKey })
	}
	s.remapEcho(b)
	if at < 0 {
		s.Blocks = append(s.Blocks, b)
	} else {
		s.Blocks[at] = b
	}
}

// Reduce never mutates its input or reads time. Commands carry intents for the
// UI's effect runner, and every async response must carry its issuing Gen.
func Reduce(s State, m Msg) (State, []Cmd) {
	s = s.clone()
	switch m := m.(type) {
	case Snapshot:
		if !epoch(m.Epoch) || !epoch(m.Revocation) || m.Topic == "" || compare(m.Epoch, s.Epoch) < 0 || compare(m.Revocation, s.Revocation) < 0 {
			return s, nil
		}
		t, exists := s.Topics[m.Topic]
		changedRevocation := s.Revocation != "" && m.Revocation != s.Revocation
		if m.Epoch == s.Epoch && !changedRevocation && exists && (m.Cursor < t.Cursor || (m.Cursor == t.Cursor && !t.OutOfSync)) {
			return s, nil
		}
		s.setEpoch(m.Epoch)
		if changedRevocation {
			s.Blocks = nil
			s.Folds = nil
			s.Selected = ""
			s.Scroll = Position{}
			s.Topics = nil
			s.pending = nil
			s.Busy = false
			s.Stopping = false
			s.Gen++
		}
		s.Revocation = m.Revocation
		// Replace only this topic, preserving other topics and echo positions.
		incoming := make(map[string]Block, len(m.Items))
		order := make([]string, 0, len(m.Items))
		for _, b := range m.Items {
			if b.ID == "" {
				continue
			}
			if old, ok := incoming[b.ID]; ok {
				if old.Revision >= b.Revision {
					continue
				}
			} else {
				order = append(order, b.ID)
			}
			b.Topic = m.Topic
			b.Local = false
			b.filter = sanitize.Stream{}
			if b.Done {
				b.Text = sanitize.Display(b.Text)
			} else {
				b.filter, b.Text = b.filter.Feed(b.Text)
			}
			incoming[b.ID] = b
		}
		kept := make([]Block, 0, len(s.Blocks)+len(incoming))
		for _, old := range s.Blocks {
			b, ok := incoming[old.ID]
			if !ok && old.Local {
				for _, id := range order {
					candidate, present := incoming[id]
					if present && candidate.EchoKey == old.EchoKey {
						b = candidate
						ok = true
						break
					}
				}
			}
			if ok {
				kept = append(kept, b)
				delete(incoming, b.ID)
				s.remapEcho(b)
			} else if old.Topic != m.Topic || old.Local {
				kept = append(kept, old)
			}
		}
		for _, id := range order {
			if b, ok := incoming[id]; ok {
				kept = append(kept, b)
				s.remapEcho(b)
			}
		}
		s.Blocks = kept
		if s.Topics == nil {
			s.Topics = map[string]Topic{}
		}
		s.Topics[m.Topic] = Topic{Cursor: m.Cursor}
		if !m.ServerTime.IsZero() && !m.ClientTime.IsZero() {
			s.Offset = m.ServerTime.Sub(m.ClientTime)
			s.ServerNow = m.ServerTime
		}
	case Delta:
		if !epoch(m.Epoch) || compare(m.Epoch, s.Epoch) < 0 || m.ID == "" || m.Topic == "" {
			return s, nil
		}
		t := s.Topics[m.Topic]
		if t.OutOfSync {
			return s, nil
		}
		if m.Epoch != s.Epoch {
			return s, s.resync(m.Topic)
		}
		if m.Cursor <= t.Cursor {
			return s, nil
		}
		if m.Cursor != t.Cursor+1 {
			return s, s.resync(m.Topic)
		}
		if s.Topics == nil {
			s.Topics = map[string]Topic{}
		}
		s.Topics[m.Topic] = Topic{Cursor: m.Cursor}
		at := s.find(m.ID)
		b := Block{ID: m.ID, Topic: m.Topic, Kind: protocol.KindOutputDelta}
		if at >= 0 {
			b = s.Blocks[at]
			if b.Done || b.Topic != m.Topic || b.Revision >= m.Revision {
				return s, nil
			}
		}
		var text string
		b.filter, text = b.filter.Feed(m.Text)
		b.Text += text
		b.Revision = m.Revision
		s.put(b)
	case ItemDone:
		if !epoch(m.Epoch) || compare(m.Epoch, s.Epoch) < 0 || m.Block.ID == "" || m.Topic == "" {
			return s, nil
		}
		t := s.Topics[m.Topic]
		if t.OutOfSync {
			return s, nil
		}
		if m.Epoch != s.Epoch {
			return s, s.resync(m.Topic)
		}
		if m.Cursor <= t.Cursor {
			return s, nil
		}
		if m.Cursor != t.Cursor+1 {
			return s, s.resync(m.Topic)
		}
		if s.Topics == nil {
			s.Topics = map[string]Topic{}
		}
		s.Topics[m.Topic] = Topic{Cursor: m.Cursor}
		b := m.Block
		if at := s.find(b.ID); at >= 0 && s.Blocks[at].Revision >= b.Revision {
			return s, nil
		}
		b.Topic = m.Topic
		b.Done = true
		b.Local = false
		b.Text = sanitize.Display(b.Text)
		b.filter = sanitize.Stream{}
		s.put(b)
		s.Stopping = false
	case Frame:
		var decoder protocol.ClientDecoder
		e, err := decoder.Decode(m.Data)
		if err != nil {
			s.Malformed++
			s.Placeholder = protocol.ErrorMalformed
			return s, nil
		}
		if e == nil {
			s.Unknown++
			return s, nil
		}
		if compare(e.Epoch, s.Epoch) < 0 {
			return s, nil
		}
		// Conversation bodies are reserved in client 1.0; do not infer content from
		// envelope ids, diagnostic strings, or the empty typed payloads.
		if w, ok := e.Payload.(protocol.Welcome); ok {
			if w.Epoch != e.Epoch {
				s.Malformed++
				s.Placeholder = protocol.ErrorMalformed
				return s, nil
			}
			s.setEpoch(w.Epoch)
			s.Capabilities = slices.Clone(w.Capabilities)
			s.Agents = slices.Clone(w.Agents)
			for i := range s.Agents {
				s.Agents[i].Name = sanitize.Display(s.Agents[i].Name)
			}
		}
		if e.Error != nil {
			s.Placeholder = e.Error.Code
		}
	case Select:
		s.Selected = m.ID
	case Fold:
		if s.Folds == nil {
			s.Folds = map[string]bool{}
		}
		s.Folds[m.ID] = m.Closed
	case Scroll:
		s.Scroll = Position{ID: m.ID, Row: max(0, m.Row)}
	case Notice:
		s.notice(m)
	case EditDraft:
		s.Draft = cloneDraft(m.Draft)
	case Send:
		if s.Busy || m.Key == "" || (s.Draft.Text == "" && len(s.Draft.Attachments) == 0) || s.find(m.Key) >= 0 {
			return s, nil
		}
		s.Gen++
		s.Busy = true
		req := SendRequest{Gen: s.Gen, Key: m.Key, Draft: cloneDraft(s.Draft)}
		s.pending = &req
		s.Draft = Draft{}
		s.put(Block{ID: m.Key, EchoKey: m.Key, Text: sanitize.Display(req.Draft.Text), Local: true})
		return s, []Cmd{func(context.Context) Msg { copy := req; copy.Draft = cloneDraft(req.Draft); return copy }}
	case SendResult:
		if s.pending == nil || m.Gen != s.Gen || m.Gen != s.pending.Gen || m.Key != s.pending.Key {
			return s, nil
		}
		if m.Failed {
			s.restoreDraft()
		}
		s.pending = nil
		s.Busy = false
	case Interrupt:
		if s.Stopping {
			return s, nil
		}
		s.Stopping = true
		req := InterruptRequest{Gen: s.Gen}
		return s, []Cmd{func(context.Context) Msg { return req }}
	case Tick:
		s.ServerNow = m.Now.Add(s.Offset)
	}
	return s, nil
}

// Only called on a cloned state. Draft text typed since send remains last.
func (s *State) restoreDraft() {
	d := cloneDraft(s.pending.Draft)
	if d.Text != "" && s.Draft.Text != "" {
		d.Text += "\n"
	}
	d.Text += s.Draft.Text
	d.Attachments = append(d.Attachments, s.Draft.Attachments...)
	s.Draft = d
	key := s.pending.Key
	s.Blocks = slices.DeleteFunc(s.Blocks, func(b Block) bool { return b.Local && b.EchoKey == key })
	s.pending = nil
	s.Busy = false
}
