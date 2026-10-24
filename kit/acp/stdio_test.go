// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}
func TestStdioE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	l := lease.New(func() { close(released) })
	defer l.Release()
	cwd := t.TempDir()
	c, err := acp.Start(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestStdioAgentProcess$"}, Dir: cwd, Env: []string{"LINA_ACP_TEST_CHILD=1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	initialize(t, c, ctx)
	s, err := c.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	complete := make(chan error, 1)
	go func() {
		r, e := c.Prompt(ctx, acp.PromptRequest{SessionID: s.SessionID, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "Write result.txt"}}}})
		if e == nil && r.StopReason != acp.StopReasonEndTurn {
			e = fmt.Errorf("stop %s", r.StopReason)
		}
		complete <- e
	}()
	select {
	case p := <-c.Permissions():
		if p == nil {
			t.Fatalf("permission connection closed: %v", c.Err())
		}
		if err = p.Answer(ctx, "once"); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err = <-complete:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, kind := range []string{"tool_call", "tool_call_update", "agent_message_chunk"} {
		select {
		case u := <-c.Updates():
			b, e := json.Marshal(u.Notification.Update)
			if e != nil {
				t.Fatal(e)
			}
			var tag struct {
				Kind string `json:"sessionUpdate"`
			}
			json.Unmarshal(b, &tag)
			if tag.Kind != kind {
				t.Fatalf("progress %s", b)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	b, err := os.ReadFile(filepath.Join(cwd, "result.txt"))
	if err != nil || string(b) != "verified result\n" {
		t.Fatalf("artifact %q: %v", b, err)
	}
	// EOF/process exit cannot complete a second prompt successfully.
	_, err = c.Prompt(ctx, acp.PromptRequest{SessionID: s.SessionID, Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "exit"}}}})
	if err == nil {
		t.Fatal("process exit reported success")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	l.Release()
	select {
	case <-released:
	default:
		t.Fatal("Close returned with owned process activity still live")
	}
}
func TestStdioAgentProcess(t *testing.T) {
	if os.Getenv("LINA_ACP_TEST_CHILD") != "1" {
		return
	}
	read := rpc.NewReader(os.Stdin)
	send := func(s string) {
		if _, e := fmt.Fprintln(os.Stdout, s); e != nil {
			os.Exit(11)
		}
	}
	recv := func() frame {
		b, e := read.Read()
		if e != nil {
			os.Exit(12)
		}
		var f frame
		if json.Unmarshal(b, &f) != nil {
			os.Exit(13)
		}
		return f
	}
	reply := func(f frame, result string) { send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, f.ID, result)) }
	f := recv()
	if f.Method != "initialize" {
		os.Exit(14)
	}
	reply(f, `{"protocolVersion":1}`)
	f = recv()
	var newSession acp.NewSessionRequest
	json.Unmarshal(f.Params, &newSession)
	cwd, _ := os.Getwd()
	if f.Method != "session/new" || newSession.Cwd != cwd {
		os.Exit(15)
	}
	reply(f, `{"sessionId":"stdio"}`)
	prompt := recv()
	if prompt.Method != "session/prompt" {
		os.Exit(16)
	}
	send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"stdio","update":{"sessionUpdate":"tool_call","toolCallId":"write","title":"Write result","kind":"edit","status":"pending"}}}`)
	send(`{"jsonrpc":"2.0","id":null,"method":"session/request_permission","params":{"sessionId":"stdio","toolCall":{"toolCallId":"write"},"options":[{"optionId":"once","kind":"allow_once","name":"Once"}]}}`)
	f = recv()
	var response acp.RequestPermissionResponse
	json.Unmarshal(f.Result, &response)
	if string(f.ID) != "null" || response.Outcome.Selected == nil || response.Outcome.Selected.OptionID != "once" {
		os.Exit(17)
	}
	if os.WriteFile("result.txt", []byte("verified result\n"), 0600) != nil {
		os.Exit(18)
	}
	send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"stdio","update":{"sessionUpdate":"tool_call_update","toolCallId":"write","status":"completed"}}}`)
	send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"stdio","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Created result.txt"}}}}`)
	reply(prompt, `{"stopReason":"end_turn"}`)
	recv()
	os.Exit(0)
}
