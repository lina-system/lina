// SPDX-License-Identifier: Apache-2.0
package claudeproc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
)

func scripted(lines ...string) *stream {
	return &stream{ctx: context.Background(), scanner: bufio.NewScanner(strings.NewReader(strings.Join(lines, "\n") + "\n")), tools: map[string]bool{}, meta: provider.Meta{Class: provider.AgentRuntime}}
}

const emptyInit = `{"type":"init","id":"turn","tools":[],"mcp_servers":[],"plugins":[]}`

func TestWireRequiresApprovedInitAndSuccessfulTerminal(t *testing.T) {
	for name, lines := range map[string][]string{
		"no init":             {`{"type":"delta","id":"turn","text":"untrusted"}`},
		"foreign tool":        {`{"type":"init","id":"turn","tools":["Bash"],"mcp_servers":[],"plugins":[]}`},
		"foreign server":      {`{"type":"init","id":"turn","tools":[],"mcp_servers":[{"name":"other"}],"plugins":[]}`},
		"foreign plugin":      {`{"type":"init","id":"turn","tools":[],"mcp_servers":[],"plugins":[{}]}`},
		"missing declaration": {`{"type":"init","id":"turn"}`},
		"wrong id":            {`{"type":"init","id":"other","tools":[],"mcp_servers":[],"plugins":[]}`},
		"EOF":                 {emptyInit},
		"failed":              {emptyInit, `{"type":"terminal","id":"turn","reason":"failed"}`},
		"negative usage":      {emptyInit, `{"type":"usage","id":"turn","usage":{"input":-1}}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := scripted(lines...).Next(t.Context()); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("bad stream accepted: %v", err)
			}
		})
	}
	s := scripted(emptyInit,
		`{"type":"usage","id":"turn","usage":{"input":0}}`,
		`{"type":"item_done","id":"turn","item":{"role":"assistant","content":[{"type":"text","text":"fixture"}]}}`,
		`{"type":"terminal","id":"turn","reason":"completed","session_id":"session","cost_estimate":{"currency":"USD","micros":5}}`)
	event, err := s.Next(t.Context())
	if err != nil || event.Kind != provider.ItemDone || !strings.Contains(string(event.Item), "output_text") {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if event, err = s.Next(t.Context()); err != nil || event.Kind != provider.Terminal {
		t.Fatal("missing terminal")
	}
	if _, err := s.Next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	meta := s.Meta()
	if meta.Usage.Input == nil || *meta.Usage.Input != 0 || meta.Usage.Output != nil || meta.RuntimeSessionID != "session" {
		t.Fatalf("meta=%+v", meta)
	}
}

type bridgeFunc func(context.Context, provider.ToolCall) (provider.ToolResult, error)

func (f bridgeFunc) Call(ctx context.Context, c provider.ToolCall) (provider.ToolResult, error) {
	return f(ctx, c)
}
func TestWireReturnsCoreToolReceipt(t *testing.T) {
	s := scripted(`{"type":"init","id":"turn","tools":["mcp__lina__lookup"],"mcp_servers":[{"name":"lina","source":"sdk","status":"connected"}],"plugins":[]}`,
		`{"type":"tool_call","id":"turn","call_id":"call","name":"lookup","arguments":{"key":"fixture"}}`,
		`{"type":"terminal","id":"turn","reason":"completed"}`)
	s.tools["lookup"] = true
	s.request.Bridge = bridgeFunc(func(_ context.Context, c provider.ToolCall) (provider.ToolResult, error) {
		if c.ID != "call" || c.Name != "lookup" || c.Arguments != `{"key":"fixture"}` {
			t.Fatal(c)
		}
		return provider.ToolResult{ID: c.ID, Output: json.RawMessage(`{"code":"ok"}`)}, nil
	})
	reader, writer := io.Pipe()
	s.stdin = writer
	defer reader.Close()
	defer writer.Close()
	received := make(chan map[string]any, 1)
	go func() { var message map[string]any; _ = json.NewDecoder(reader).Decode(&message); received <- message }()
	event, err := s.Next(t.Context())
	if err != nil || event.Kind != provider.Terminal {
		t.Fatal(err)
	}
	message := <-received
	if message["type"] != "tool_result" || message["call_id"] != "call" || message["id"] != "turn" {
		t.Fatal(message)
	}
}
