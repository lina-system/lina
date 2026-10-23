// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/rpc"
)

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acp.Error      `json:"error,omitempty"`
}
type agent struct {
	conn net.Conn
	t    *testing.T
	ctx  context.Context
	tr   rpc.Transport
}

func (a agent) read() frame {
	a.t.Helper()
	b, e := a.tr.Read(a.ctx)
	if e != nil {
		a.t.Fatal(e)
	}
	var f frame
	if e = json.Unmarshal(b, &f); e != nil {
		a.t.Fatal(e)
	}
	if f.JSONRPC != "2.0" {
		a.t.Fatalf("JSON-RPC version: %s", b)
	}
	return f
}
func (a agent) send(s string) {
	a.t.Helper()
	if e := a.tr.Write(a.ctx, json.RawMessage(s)); e != nil {
		a.t.Fatal(e)
	}
}
func (a agent) reply(f frame, result string) {
	a.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, f.ID, result))
}
func (a agent) method(want string) frame {
	a.t.Helper()
	f := a.read()
	if f.Method != want {
		a.t.Fatalf("method %q, want %q", f.Method, want)
	}
	return f
}
func (a agent) initialize() {
	f := a.method("initialize")
	var p struct {
		ProtocolVersion    int
		ClientCapabilities struct {
			FS          json.RawMessage
			Terminal    bool
			Elicitation struct {
				Form json.RawMessage
				URL  json.RawMessage
			}
		}
	}
	if e := json.Unmarshal(f.Params, &p); e != nil {
		a.t.Fatal(e)
	}
	if p.ProtocolVersion != 1 || p.ClientCapabilities.Terminal || len(p.ClientCapabilities.FS) > 0 || len(p.ClientCapabilities.Elicitation.Form) == 0 || len(p.ClientCapabilities.Elicitation.URL) > 0 {
		a.t.Fatalf("capabilities %s", f.Params)
	}
	a.reply(f, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
}
func fixture(t *testing.T, script func(agent)) (*acp.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	left, right := net.Pipe()
	c := acp.New(ctx, rpc.NewStream(left))
	done := make(chan struct{})
	go func() { defer close(done); defer right.Close(); script(agent{right, t, ctx, rpc.NewStream(right)}) }()
	t.Cleanup(func() { cancel(); c.Close(); right.Close(); <-done })
	return c, ctx
}
func initialize(t *testing.T, c *acp.Client, ctx context.Context) {
	t.Helper()
	if _, e := c.Initialize(ctx, acp.Implementation{Name: "test", Version: "1"}); e != nil {
		t.Fatal(e)
	}
}
func TestInitializeVersion(t *testing.T) {
	for _, v := range []int{0, 1, 2, 99} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			c, ctx := fixture(t, func(a agent) {
				f := a.method("initialize")
				a.reply(f, fmt.Sprintf(`{"protocolVersion":%d}`, v))
				<-a.ctx.Done()
			})
			_, e := c.Initialize(ctx, acp.Implementation{Name: "test", Version: "1"})
			if (e == nil) != (v == 1) {
				t.Fatalf("version %d: %v", v, e)
			}
			if v != 1 {
				if _, e = c.NewSession(ctx, acp.NewSessionRequest{Cwd: "/work", McpServers: []acp.McpServer{}}); e == nil {
					t.Fatal("used rejected connection")
				}
			}
		})
	}
}
func TestPromptStopReasonsAndUpdates(t *testing.T) {
	stops := []acp.StopReason{acp.StopReasonEndTurn, acp.StopReasonMaxTokens, acp.StopReasonMaxTurnRequests, acp.StopReasonRefusal, acp.StopReasonCancelled}
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		f := a.method("session/new")
		var p acp.NewSessionRequest
		if e := json.Unmarshal(f.Params, &p); e != nil || p.Cwd != "/work" || p.McpServers == nil {
			t.Fatalf("new %s: %v", f.Params, e)
		}
		a.reply(f, `{"sessionId":"s"}`)
		for _, stop := range stops {
			f = a.method("session/prompt")
			var p acp.PromptRequest
			if e := json.Unmarshal(f.Params, &p); e != nil || p.SessionID != "s" || len(p.Prompt) != 1 || p.Prompt[0].Text.Text != "work" {
				t.Fatalf("prompt %s: %v", f.Params, e)
			}
			a.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"result"}}}}`)
			a.reply(f, fmt.Sprintf(`{"stopReason":%q}`, stop))
		}
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	s, e := c.NewSession(ctx, acp.NewSessionRequest{Cwd: "/work", McpServers: []acp.McpServer{}})
	if e != nil || s.SessionID != "s" {
		t.Fatalf("new: %+v %v", s, e)
	}
	for _, stop := range stops {
		r, e := c.Prompt(ctx, acp.PromptRequest{SessionID: s.SessionID, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}})
		if e != nil || r.StopReason != stop {
			t.Fatalf("stop: %+v %v", r, e)
		}
		select {
		case u := <-c.Updates():
			if u.Replay || u.Notification.Update.AgentMessageChunk.Content.Text.Text != "result" {
				t.Fatalf("update %+v", u)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func TestLoadReplayBoundary(t *testing.T) {
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		f := a.method("session/load")
		for _, sid := range []string{"s", "other"} {
			a.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"future","n":9007199254740993}}}`, sid))
		}
		a.reply(f, `{}`)
		a.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"future","n":9007199254740993}}}`)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	if _, e := c.LoadSession(ctx, acp.LoadSessionRequest{SessionID: "s", Cwd: "/work", McpServers: []acp.McpServer{}}); e != nil {
		t.Fatal(e)
	}
	for i, want := range []bool{true, false, false} {
		select {
		case u := <-c.Updates():
			if u.Replay != want {
				t.Fatalf("update %d replay=%v", i, u.Replay)
			}
			b, e := json.Marshal(u.Notification.Update)
			if e != nil || string(b) != `{"sessionUpdate":"future","n":9007199254740993}` {
				t.Fatalf("unknown changed: %s %v", b, e)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func permission(id, sid string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/request_permission","params":{"sessionId":%q,"toolCall":{"toolCallId":"tool"},"options":[{"optionId":"yes","name":"Once","kind":"allow_once"},{"optionId":"no","name":"Deny","kind":"reject_once"}]}}`, id, sid)
}
func TestPermissionOnceAndCancel(t *testing.T) {
	verified := make(chan struct{})
	ids := []string{`"approval"`, `9007199254740993`, `"9007199254740993"`}
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		a.send(permission(ids[0], "s"))
		f := a.read()
		if string(f.ID) != ids[0] || string(f.Result) != `{"outcome":{"optionId":"yes","outcome":"selected"}}` {
			t.Fatalf("answer %s %s", f.ID, f.Result)
		}
		for _, id := range ids[1:] {
			a.send(permission(id, "s"))
		}
		seen := map[string]bool{}
		for range 3 {
			f = a.read()
			if f.Method == "session/cancel" {
				continue
			}
			if string(f.Result) != `{"outcome":{"outcome":"cancelled"}}` {
				t.Fatalf("cancel %s", f.Result)
			}
			seen[string(f.ID)] = true
		}
		if !seen[ids[1]] || !seen[ids[2]] {
			t.Fatalf("ids %v", seen)
		}
		// A request already in flight when cancel was sent is cancelled too.
		a.send(permission(`"late"`, "s"))
		f = a.read()
		if string(f.Result) != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("late %s", f.Result)
		}
		close(verified)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	next := func() *acp.Permission {
		select {
		case p := <-c.Permissions():
			return p
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	p := next()
	if e := p.Answer(ctx, "yes"); e != nil {
		t.Fatal(e)
	}
	if e := p.Answer(ctx, "yes"); !errors.Is(e, acp.ErrAnswered) {
		t.Fatalf("duplicate: %v", e)
	}
	p1, p2 := next(), next()
	if e := c.Cancel(ctx, "s"); e != nil {
		t.Fatal(e)
	}
	for _, p := range []*acp.Permission{p1, p2} {
		if e := p.Answer(ctx, "no"); !errors.Is(e, acp.ErrAnswered) {
			t.Fatalf("cancelled answer: %v", e)
		}
	}
	select {
	case <-verified:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestUnsupportedRequestsAndTypedError(t *testing.T) {
	ready := make(chan struct{})
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		for i, m := range []string{"fs/read_text_file", "fs/write_text_file", "terminal/create", "terminal/output", "terminal/release", "terminal/wait_for_exit", "terminal/kill", "future/request"} {
			a.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":"%d","method":%q,"params":{}}`, i, m))
			f := a.read()
			if f.Error == nil || f.Error.Code != -32601 || string(f.ID) != fmt.Sprintf(`"%d"`, i) {
				t.Fatalf("unsupported: %+v", f)
			}
		}
		close(ready)
		f := a.method("session/new")
		a.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"Sign in","data":{"retry":false}}}`, f.ID))
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	<-ready
	_, e := c.NewSession(ctx, acp.NewSessionRequest{Cwd: "/work", McpServers: []acp.McpServer{}})
	var rpcErr *acp.RPCError
	if !errors.As(e, &rpcErr) || rpcErr.Code != -32000 {
		t.Fatalf("typed error: %v", e)
	}
}
