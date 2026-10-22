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
	ShellFileToolsAvailable  *bool
	Card                     *Card
	ID, Topic, EchoKey, Text string
	Speaker                  protocol.ConversationSpeaker
	Body, Title              string
	InlineReply              bool
	RunID                    string
	TurnKnown, TurnActive    bool
	Revision                 uint64
	Kind                     protocol.Kind
	Done, Local              bool
	Removed                  bool
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

type InputHistoryEntry struct{ ID, Text string }

// InputHistory derives recall from the current authoritative conversation.
// Pending echoes and form fields are not accepted person inputs. Keeping no
// second text store also lets snapshots and revocations remove history entries.
func (s State) InputHistory(conversation string) []InputHistoryEntry {
	var entries []InputHistoryEntry
	if conversation == "" {
		return entries
	}
	for i := len(s.Blocks) - 1; i >= 0; i-- {
		b := s.Blocks[i]
		if b.Topic == "conversation:"+conversation && b.Speaker == protocol.ConversationSpeakerPerson &&
			b.Done && !b.Local && !b.Removed && b.Text != "" {
			entries = append(entries, InputHistoryEntry{ID: b.ID, Text: b.Text})
		}
	}
	return entries
}

type Position struct {
	ID  string
	Row int
}
type State struct {
	CardSequence       uint64
	Cards              []Card
	CardView           CardView
	Epoch, Revocation  string
	Topics             map[string]Topic
	Blocks             []Block
	Selected           string
	Folds              map[string]bool
	Scroll             Position
	Notices            []Notice
	Draft              Draft
	Busy, Stopping     bool
	TurnActive         bool
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
	Notify                   bool
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
	Notify       bool
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
type InputMode string

const (
	InputNormal   InputMode = "input"
	InputSteering InputMode = "steering"
	InputQueue    InputMode = "queue"
)

type Send struct {
	Key   string
	Queue bool
}
type Turn struct{ Active bool }
type ConnectionLost struct{}

func (ConnectionLost) isMsg() {}

func (Turn) isMsg() {}

type SendRequest struct {
	Gen   uint64
	Key   string
	Draft Draft
	Mode  InputMode
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
	s.Cards = slices.Clone(s.Cards)
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

// setEpoch returns topics whose old-generation subscriptions need resync.
// Schedule requests after the caller finishes applying its state changes so
// they carry the final generation and can omit an already applied snapshot.
func (s *State) setEpoch(e string) []string {
	if e == s.Epoch {
		return nil
	}
	if s.Epoch != "" {
		s.notice(Notice{ID: "epoch:" + e, Code: "epoch_changed"})
	}
	for i := range s.Cards {
		if s.Cards[i].Outcome == "" {
			s.Cards[i].Outcome = CardStopped
		}
		s.Cards[i].pending = nil
	}
	s.CardView = CardView{}
	s.Epoch = e
	if s.pending != nil {
		s.restoreDraft()
	}
	s.Stopping = false
	s.TurnActive = false
	s.Gen++
	topics := slices.Sorted(maps.Keys(s.Topics))
	for _, name := range topics {
		s.Topics[name] = Topic{}
	}
	return topics
}
func (s *State) resyncTopics(topics []string, applied string) []Cmd {
	var cmds []Cmd
	for _, topic := range topics {
		if topic != applied {
			cmds = append(cmds, s.resync(topic)...)
		}
	}
	return cmds
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
	case FocusConversation:
		return focusConversation(s), nil
	case Snapshot:
		if !epoch(m.Epoch) || !epoch(m.Revocation) || m.Topic == "" || compare(m.Epoch, s.Epoch) < 0 || compare(m.Revocation, s.Revocation) < 0 {
			return s, nil
		}
		t, exists := s.Topics[m.Topic]
		changedRevocation := s.Revocation != "" && m.Revocation != s.Revocation
		if m.Epoch == s.Epoch && !changedRevocation && exists && (m.Cursor < t.Cursor || (m.Cursor == t.Cursor && !t.OutOfSync)) {
			return s, nil
		}
		invalidated := s.setEpoch(m.Epoch)
		if changedRevocation {
			if s.pending != nil {
				s.restoreDraft()
			}
			s.Cards = nil
			s.CardView = CardView{}
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
		// Replace only this topic in snapshot order. Existing blocks and local
		// echoes anchor the merge, but cannot override the authoritative order.
		incoming := make(map[string]Block, len(m.Items))
		order := make([]string, 0, len(m.Items))
		positions := make(map[string]int, len(m.Items))
		for _, b := range m.Items {
			if b.ID == "" {
				continue
			}
			if old, ok := incoming[b.ID]; ok {
				if old.Revision >= b.Revision {
					continue
				}
			} else {
				positions[b.ID] = len(order)
				order = append(order, b.ID)
			}
			b.Topic = m.Topic
			if b.Card != nil {
				card := cleanCard(*b.Card)
				card.Topic = m.Topic
				b.Card = &card
			}
			b.Local = false
			b.Body = sanitize.Display(b.Body)
			b.filter = sanitize.Stream{}
			if b.Done {
				b.Text = sanitize.Display(b.Text)
			} else {
				b.filter, b.Text = b.filter.Feed(b.Text)
			}
			incoming[b.ID] = b
		}
		kept := make([]Block, 0, len(s.Blocks)+len(incoming))
		next := 0
		appendThrough := func(end int) {
			for next <= end {
				b := incoming[order[next]]
				kept = append(kept, b)
				s.remapEcho(b)
				next++
			}
		}
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
				// Include any missing predecessors before this surviving block.
				// An already emitted block is simply consumed at its old slot.
				appendThrough(positions[b.ID])
			} else if old.Topic != m.Topic || old.Local {
				kept = append(kept, old)
			}
		}
		appendThrough(len(order) - 1)
		s.Blocks = kept
		if s.Topics == nil {
			s.Topics = map[string]Topic{}
		}
		s.Topics[m.Topic] = Topic{Cursor: m.Cursor}
		if !m.ServerTime.IsZero() && !m.ClientTime.IsZero() {
			s.Offset = m.ServerTime.Sub(m.ClientTime)
			s.ServerNow = m.ServerTime
		}
		for i := range s.Cards {
			if s.Cards[i].Topic != m.Topic || s.Cards[i].Outcome != "" {
				continue
			}
			found := false
			for _, b := range s.Blocks {
				if b.Topic == m.Topic && b.Card != nil && b.Card.ID == s.Cards[i].ID {
					found = true
					break
				}
			}
			if !found {
				s.Cards[i].Outcome = CardWithdrawn
				s.Cards[i].pending = nil
				if s.CardView.ID == s.Cards[i].ID {
					s.CardView = CardView{ID: s.CardView.ID}
				}
			}
		}
		cmds := s.resyncTopics(invalidated, m.Topic)
		for _, b := range s.Blocks {
			if b.Topic == m.Topic && b.Card != nil {
				var emitted []Cmd
				s, emitted = s.reduceCard(PutCard{Card: *b.Card, Notify: m.Notify})
				cmds = append(cmds, emitted...)
			}
		}
		return s, cmds
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
		if at := s.find(b.ID); at >= 0 {
			old := s.Blocks[at]
			if old.Revision > b.Revision || (old.Revision == b.Revision && old.Done) {
				return s, nil
			}
		}
		if b.Removed && m.Topic == "running" {
			s.Blocks = slices.DeleteFunc(s.Blocks, func(old Block) bool { return old.ID == b.ID && old.Topic == m.Topic })
			return s, nil
		}
		b.Topic = m.Topic
		if b.Card != nil {
			card := cleanCard(*b.Card)
			card.Topic = m.Topic
			b.Card = &card
		}
		b.Done = true
		b.Local = false
		b.Text = sanitize.Display(b.Text)
		b.Body = sanitize.Display(b.Body)
		b.filter = sanitize.Stream{}
		s.put(b)
		s.Stopping = false
		if b.Card != nil {
			return s.reduceCard(PutCard{Card: *b.Card, Notify: m.Notify})
		}
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
		var invalidated []string
		if w, ok := e.Payload.(protocol.Welcome); ok {
			if w.Epoch != e.Epoch {
				s.Malformed++
				s.Placeholder = protocol.ErrorMalformed
				return s, nil
			}
			invalidated = s.setEpoch(w.Epoch)
			s.Capabilities = slices.Clone(w.Capabilities)
			running := slices.ContainsFunc(w.Capabilities, func(c protocol.Capability) bool { return c.Name == "running" })
			waiting := slices.ContainsFunc(w.Capabilities, func(c protocol.Capability) bool { return c.Name == "waiting" })
			code := ""
			switch {
			case !running && !waiting:
				code = "shell.lists_unavailable"
			case !running:
				code = "shell.running_unavailable"
			case !waiting:
				code = "shell.waiting_unavailable"
			}
			if code != "" {
				s.notice(Notice{ID: code, Code: code})
			}
			s.Agents = slices.Clone(w.Agents)
			for i := range s.Agents {
				s.Agents[i].Name = sanitize.Display(s.Agents[i].Name)
			}
		}
		if e.Error != nil {
			s.Placeholder = e.Error.Code
		}
		return s, s.resyncTopics(invalidated, "")
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
		req.Mode = InputNormal
		if s.TurnActive {
			req.Mode = InputSteering
		}
		if m.Queue {
			req.Mode = InputQueue
		}
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
	case ConnectionLost:
		if s.pending != nil {
			s.restoreDraft()
			s.Gen++
		}
	case Turn:
		s.TurnActive = m.Active
		if !m.Active {
			s.Stopping = false
		}
	case Interrupt:
		if s.Stopping {
			return s, nil
		}
		s.Stopping = true
		req := InterruptRequest{Gen: s.Gen}
		return s, []Cmd{func(context.Context) Msg { return req }}
	case Tick:
		s.ServerNow = m.Now.Add(s.Offset)
	default:
		return s.reduceCard(m)
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
