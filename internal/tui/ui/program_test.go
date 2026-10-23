// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

type shellCall struct {
	id      string
	payload protocol.Payload
}

// Unused request methods deliberately panic through the nil embedded interface.
// Every request exercised by the shell must go through this fake, not a socket.
type fakeShellPeer struct {
	shellClient
	requests  chan shellCall
	replies   chan protocol.Envelope
	receiving chan struct{}
	closed    atomic.Int32
	closeOnce sync.Once
	stopped   chan struct{}
	sequence  int
}

func (p *fakeShellPeer) Welcome() protocol.Welcome {
	return protocol.Welcome{MainID: "main-example", Epoch: "42", ProtocolVersion: "1.0", PersonID: "person-example", Agents: []protocol.Agent{{AgentID: "agent-one", Name: "Welcome Agent"}}, Capabilities: []protocol.Capability{
		{Name: "conversation", Version: "1.0"}, {Name: "agents", Version: "1.0"},
		{Name: "exchanges", Version: "1.0"}, {Name: "web", Version: "1.0"}, {Name: "channels", Version: "1.0"},
	}}
}
func (p *fakeShellPeer) Receive(ctx context.Context) (protocol.Envelope, error) {
	select {
	case p.receiving <- struct{}{}:
	case <-p.stopped:
		return protocol.Envelope{}, io.EOF
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	}
	select {
	case reply := <-p.replies:
		return reply, nil
	case <-p.stopped:
		return protocol.Envelope{}, io.EOF
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	}
}
func (p *fakeShellPeer) Close() error {
	p.closed.Add(1)
	p.closeOnce.Do(func() { close(p.stopped) })
	return nil
}
func (p *fakeShellPeer) request(ctx context.Context, payload protocol.Payload) (string, error) {
	p.sequence++
	id := fmt.Sprintf("request-%d", p.sequence)
	select {
	case p.requests <- shellCall{id, payload}:
		return id, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (p *fakeShellPeer) Agent(ctx context.Context, v protocol.AgentRequest) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) History(ctx context.Context, v protocol.HistoryGet) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) Subscribe(ctx context.Context, v protocol.Subscribe) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) Talk(ctx context.Context, v protocol.AgentTalkGet) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) ExpandTalk(ctx context.Context, v protocol.AgentTalkExpand) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) Web(ctx context.Context, v protocol.WebRequest) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) Channel(ctx context.Context, v protocol.ChannelRequest) (string, error) {
	return p.request(ctx, v)
}
func (p *fakeShellPeer) Input(ctx context.Context, v protocol.Input) (string, error) {
	return p.request(ctx, v)
}

type shellOutput struct {
	mu    sync.Mutex
	frame string
}

func (o *shellOutput) Write(b []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.frame = string(b)
	return len(b), nil
}
func (o *shellOutput) text() string { o.mu.Lock(); defer o.mu.Unlock(); return o.frame }

func nextShellCall(t *testing.T, ctx context.Context, p *fakeShellPeer) shellCall {
	t.Helper()
	select {
	case call := <-p.requests:
		return call
	case <-ctx.Done():
		t.Fatal("shell did not send request:", ctx.Err())
		return shellCall{}
	}
}
func shellReceiveBarrier(t *testing.T, ctx context.Context, p *fakeShellPeer) {
	t.Helper()
	// Receive is called again only after handle has acknowledged the last frame.
	select {
	case <-p.receiving:
	case <-ctx.Done():
		t.Fatal("shell did not handle reply:", ctx.Err())
	}
}
func shellReply(t *testing.T, ctx context.Context, p *fakeShellPeer, call shellCall, kind protocol.Kind, payload protocol.Payload, refusal *protocol.Error) {
	t.Helper()
	reply := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: kind, MessageID: "reply-" + call.id, AttemptID: "attempt-" + call.id, ReplyTo: &call.id, Sender: protocol.Sender{Party: protocol.PartyCore, ID: "main-example"}, Epoch: "42", SentAt: "2026-01-01T00:00:01Z", EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: payload, Error: refusal}
	// Keep the fake on the same generated, validated frame boundary as clientapi.
	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var decoder protocol.ClientDecoder
	if _, err := decoder.Decode(data); err != nil {
		t.Fatal("invalid fake reply:", err)
	}
	select {
	case p.replies <- reply:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	shellReceiveBarrier(t, ctx, p)
}

func startFakeShell(t *testing.T) (context.Context, *fakeShellPeer, *os.File, *shellOutput) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	reader, input, err := os.Pipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	p := &fakeShellPeer{stopped: make(chan struct{}), requests: make(chan shellCall, 16), replies: make(chan protocol.Envelope), receiving: make(chan struct{})}
	output := &shellOutput{}
	display := &display{output: output, interactive: true, styles: NewStyles(Options{Width: 100, Profile: Monochrome, Language: design.English})}
	done := make(chan error, 1)
	go func() {
		done <- runClientShell(ctx, func(context.Context) (connectionPeer, error) { return p, nil }, reader, display, true)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("shell shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("shell did not join")
		}
		reader.Close()
		input.Close()
		if p.closed.Load() != 1 {
			t.Errorf("peer closes = %d, want 1", p.closed.Load())
		}
	})
	shellReceiveBarrier(t, ctx, p)
	core := nextShellCall(t, ctx, p)
	if sub, ok := core.payload.(protocol.Subscribe); !ok || sub.Topic == nil || *sub.Topic != "core" {
		t.Fatalf("first request = %+v", core)
	}
	agents := nextShellCall(t, ctx, p)
	if req, ok := agents.payload.(protocol.AgentRequest); !ok || req.Operation == nil || *req.Operation != "list" {
		t.Fatalf("agent discovery = %+v", agents)
	}
	rows := []protocol.Agent{{AgentID: "agent-one", Name: "Routed Agent"}}
	shellReply(t, ctx, p, agents, protocol.KindAgentResult, protocol.AgentResult{Operation: stringPointer("list"), Agents: &rows}, nil)
	history := nextShellCall(t, ctx, p)
	if _, ok := history.payload.(protocol.HistoryGet); !ok {
		t.Fatalf("agent reply did not request history: %+v", history)
	}
	shellReply(t, ctx, p, history, protocol.KindHistory, protocol.History{ConversationID: stringPointer("main:agent-one"), AgentID: stringPointer("agent-one")}, nil)
	for _, topic := range []string{"conversation:main:agent-one", "results:main:agent-one"} {
		call := nextShellCall(t, ctx, p)
		if sub, ok := call.payload.(protocol.Subscribe); !ok || sub.Topic == nil || *sub.Topic != topic {
			t.Fatalf("history subscription = %+v, want %s", call, topic)
		}
	}
	if text := output.text(); !strings.Contains(text, "Routed Agent") {
		t.Fatalf("agent list/history not rendered: %s", text)
	}
	return ctx, p, input, output
}

func TestRunClientShellReplyRouting(t *testing.T) {
	for _, family := range []string{"talk", "web", "channels", "agents"} {
		for _, refused := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refused=%t", family, refused), func(t *testing.T) {
				ctx, p, input, output := startFakeShell(t)
				if _, err := io.WriteString(input, "/"+family+"\r"); err != nil {
					t.Fatal(err)
				}
				call := nextShellCall(t, ctx, p)
				var kind protocol.Kind
				var payload protocol.Payload
				var want string
				switch family {
				case "talk":
					if _, ok := call.payload.(protocol.AgentTalkGet); !ok {
						t.Fatalf("talk request = %+v", call)
					}
					var fixture protocol.Envelope
					if err := json.Unmarshal(streamFixture(t, "normal/agent_talk"), &fixture); err != nil {
						t.Fatal(err)
					}
					kind, payload = fixture.Kind, fixture.Payload
					want = (*payload.(protocol.AgentTalk).Entries)[0].Subject
				case "web":
					if req, ok := call.payload.(protocol.WebRequest); !ok || req.Operation != "status" {
						t.Fatalf("web request = %+v", call)
					}
					kind, payload, want = protocol.KindWebResult, protocol.WebResult{Provider: "firecrawl", Configured: true}, design.Text(design.English, "web.configured")
				case "channels":
					if req, ok := call.payload.(protocol.ChannelRequest); !ok || req.Operation == nil || *req.Operation != "list" {
						t.Fatalf("channel request = %+v", call)
					}
					var fixture protocol.Envelope
					if err := json.Unmarshal(streamFixture(t, "normal/channel_result"), &fixture); err != nil {
						t.Fatal(err)
					}
					kind, payload = fixture.Kind, fixture.Payload
					want = (*payload.(protocol.ChannelResult).Connections)[0].Account
				case "agents":
					if req, ok := call.payload.(protocol.AgentRequest); !ok || req.Operation == nil || *req.Operation != "list" {
						t.Fatalf("management request = %+v", call)
					}
					rows := []protocol.Agent{{AgentID: "agent-two", Name: "Management Result"}}
					kind, payload, want = protocol.KindAgentResult, protocol.AgentResult{Operation: stringPointer("list"), Agents: &rows}, rows[0].Name
				}
				var refusal *protocol.Error
				if refused {
					refusal = &protocol.Error{Code: protocol.ErrorUnavailable}
					want = design.Text(design.English, "unavailable")
				}
				shellReply(t, ctx, p, call, kind, payload, refusal)
				if text := output.text(); !strings.Contains(text, want) {
					t.Fatalf("%s response not routed to its view: %s", family, text)
				}
				if family == "talk" && !refused {
					if _, err := io.WriteString(input, "\r"); err != nil {
						t.Fatal(err)
					}
					expand := nextShellCall(t, ctx, p)
					req, ok := expand.payload.(protocol.AgentTalkExpand)
					if !ok || req.ExchangeID == nil || *req.ExchangeID != "exchange-example" {
						t.Fatalf("talk expansion = %+v", expand)
					}
					events := []protocol.AgentTalkEvent{{AgentID: "agent-one", At: "2026-01-01T00:00:01Z", Kind: "message", Text: "Expanded exchange"}}
					shellReply(t, ctx, p, expand, protocol.KindAgentTalk, protocol.AgentTalk{ExchangeID: req.ExchangeID, Events: &events}, nil)
					if text := output.text(); !strings.Contains(text, events[0].Text) {
						t.Fatalf("talk expansion not routed: %s", text)
					}
				}
			})
		}
	}
}

func TestReplyTrackerConsumesOnceAndResets(t *testing.T) {
	tracker := make(replyTracker)
	var calls []uint64
	reply := func(attempt uint64, e protocol.Envelope) { calls = append(calls, attempt) }
	tracker.track("talk", 1, reply)
	tracker.track("web", 2, reply)
	tracker.track("channel", 3, reply)
	if tracker.response(protocol.Envelope{}) || tracker.response(protocol.Envelope{ReplyTo: stringPointer("unknown")}) {
		t.Fatal("untracked frame consumed")
	}
	for _, id := range []string{"channel", "talk", "web"} {
		e := protocol.Envelope{ReplyTo: &id}
		if !tracker.response(e) || tracker.response(e) {
			t.Fatalf("reply %s must be consumed exactly once", id)
		}
	}
	if !reflect.DeepEqual(calls, []uint64{3, 1, 2}) {
		t.Fatalf("reply attempts = %v", calls)
	}
	tracker.track("old", 4, reply)
	clear(tracker)
	if tracker.response(protocol.Envelope{ReplyTo: stringPointer("old")}) || len(calls) != 3 {
		t.Fatal("reset retained a pending reply")
	}
}

func TestViewRepliesIgnoreClosedAndReplacedAttempts(t *testing.T) {
	for _, family := range []string{"talk", "web", "channel"} {
		for _, status := range []string{"closed", "replaced", "current", "refused"} {
			t.Run(family+"/"+status, func(t *testing.T) {
				m := conversationShell()
				open := status != "closed"
				m.talk = talkView{open: open, busy: true, attempt: 2}
				m.web = webView{open: open, busy: true, attempt: 2}
				m.channels = channelView{open: open, busy: true, attempt: 2}
				attempt := uint64(2)
				if status == "replaced" {
					attempt = 1
				}
				var reply func(uint64, protocol.Envelope)
				var payload protocol.Payload
				switch family {
				case "talk":
					reply = m.talkReply
					rows := []protocol.AgentTalkLine{{ExchangeID: "exchange-test", Subject: "Result"}}
					payload = protocol.AgentTalk{Entries: &rows}
				case "web":
					reply = m.webReply
					payload = protocol.WebResult{Configured: true}
				case "channel":
					reply = m.channelReply
					rows := []protocol.ChannelConnection{{Account: "account-test"}}
					payload = protocol.ChannelResult{Connections: &rows}
				}
				beforeTalk, beforeWeb, beforeChannels := m.talk, m.web, m.channels
				e := protocol.Envelope{ReplyTo: stringPointer("request"), Payload: payload}
				if status == "refused" {
					e.Error = &protocol.Error{Code: protocol.ErrorUnavailable}
				}
				tracker := make(replyTracker)
				tracker.track("request", attempt, reply)
				if !tracker.response(e) || tracker.response(e) {
					t.Fatal("reply not consumed exactly once")
				}
				if status == "closed" || status == "replaced" {
					if !reflect.DeepEqual(m.talk, beforeTalk) || m.web != beforeWeb || !reflect.DeepEqual(m.channels, beforeChannels) {
						t.Fatal("stale reply changed a view")
					}
					return
				}
				var busy, applied bool
				var code string
				switch family {
				case "talk":
					busy, code, applied = m.talk.busy, m.talk.code, len(m.talk.rows) == 1
				case "web":
					busy, code, applied = m.web.busy, m.web.code, m.web.configured
				case "channel":
					busy, code, applied = m.channels.busy, m.channels.code, len(m.channels.rows) == 1
				}
				if busy || status == "current" && (!applied || code != "") || status == "refused" && (applied || code != "unavailable") {
					t.Fatalf("busy=%t applied=%t code=%q", busy, applied, code)
				}
			})
		}
	}
}

func TestDiscoverySessionReplyLifecycle(t *testing.T) {
	for _, outcome := range []string{"history", "stale", "missing", "unavailable", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			m := conversationShell()
			m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "agents", Version: "1.0"})
			m.content.Agents = []protocol.Agent{{AgentID: "chosen", Name: "Chosen"}}
			m.conversation.id = "main:chosen"
			p := &fakeShellPeer{requests: make(chan shellCall, 4), stopped: make(chan struct{})}
			s := &discoverySession{model: m}
			if err := s.start(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			call := nextShellCall(t, t.Context(), p)
			if _, ok := call.payload.(protocol.AgentRequest); !ok {
				t.Fatalf("discovery request = %+v", call)
			}
			rows := []protocol.Agent{{AgentID: "chosen", Name: "\x1b[31mChosen\x1b[0m"}}
			e := protocol.Envelope{ReplyTo: &call.id, Payload: protocol.AgentResult{Operation: stringPointer("list"), Agents: &rows}}
			switch outcome {
			case "stale":
				m.conversation.id = "main:other"
			case "missing":
				rows = nil
			case "unavailable":
				e.Error = &protocol.Error{Code: protocol.ErrorUnavailable}
			case "malformed":
				e.Payload = protocol.AgentResult{}
			}
			consumed, redraw, err := s.response(t.Context(), p, p, e)
			if !consumed || s.agentListID != "" {
				t.Fatal("discovery reply not consumed")
			}
			if outcome == "malformed" {
				if !errors.Is(err, errStream) {
					t.Fatalf("malformed response error = %v", err)
				}
				return
			}
			if err != nil || redraw != (outcome != "stale") {
				t.Fatalf("redraw=%t err=%v", redraw, err)
			}
			switch outcome {
			case "history":
				history := nextShellCall(t, t.Context(), p)
				req, ok := history.payload.(protocol.HistoryGet)
				if !ok || req.AgentID == nil || *req.AgentID != "chosen" || m.content.Agents[0].Name != "Chosen" {
					t.Fatalf("history request=%+v agents=%+v", history, m.content.Agents)
				}
				consumed, _, err := s.response(t.Context(), p, p, protocol.Envelope{ReplyTo: &history.id, Payload: protocol.History{ConversationID: stringPointer("main:chosen")}})
				if consumed || err != nil || s.historyID != "" {
					t.Fatal("successful history must continue to normal history routing")
				}
			case "missing":
				if !m.conversation.agentMissing || m.conversation.agentPending {
					t.Fatal("missing selection was not held")
				}
			case "unavailable":
				if p.closed.Load() != 1 || m.status.State != Recovering || !m.conversation.agentPending {
					t.Fatal("unavailable discovery must reconnect without losing selection")
				}
			}
			if len(p.requests) != 0 {
				t.Fatal("unexpected discovery request")
			}
			s.reset()
			if s.agentListID != "" || s.historyID != "" || s.reconnectAgent != "" || s.model != m {
				t.Fatal("reset did not retain only the model")
			}
		})
	}
}
