// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestCancelledWaitDoesNotRetryOrCancelTurn(t *testing.T) {
	sent := make(chan struct{})
	finish := make(chan struct{})
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		f := a.method("session/prompt")
		close(sent)
		a.method("session/cancel")
		a.reply(f, `{"stopReason":"cancelled"}`)
		close(finish)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	wait, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	p := acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}}
	go func() { _, e := c.Prompt(wait, p); result <- e }()
	<-sent
	stop()
	if e := <-result; !errors.Is(e, context.Canceled) {
		t.Fatalf("wait %v", e)
	}
	if _, e := c.Prompt(ctx, p); !errors.Is(e, acp.ErrBusy) {
		t.Fatalf("overlapping prompt %v", e)
	}
	if e := c.Cancel(ctx, "s"); e != nil {
		t.Fatal(e)
	}
	<-finish
}
func TestMalformedRepliesAndEOF(t *testing.T) {
	for _, raw := range []string{`{"jsonrpc":"2.0","result":{}}`, `{"jsonrpc":"2.0","id":2,"result":null}`, `{"jsonrpc":"2.0","id":2,"result":{},"error":{"code":-32603,"message":"bad"}}`, `{"jsonrpc":"1.0","id":2,"result":{}}`, "EOF"} {
		t.Run(raw, func(t *testing.T) {
			c, ctx := fixture(t, func(a agent) {
				a.initialize()
				a.method("session/prompt")
				if raw != "EOF" {
					a.send(raw)
				}
			})
			initialize(t, c, ctx)
			_, e := c.Prompt(ctx, acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}})
			if e == nil || errors.Is(e, context.DeadlineExceeded) {
				t.Fatalf("malformed/EOF response: %v", e)
			}
		})
	}
}
func TestUpdateOverflowFailsConnection(t *testing.T) {
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		f := a.method("session/prompt")
		for range 257 {
			a.send(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"future"}}}`)
		}
		_ = f
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	_, e := c.Prompt(ctx, acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}})
	if !errors.Is(e, acp.ErrOverflow) {
		t.Fatalf("overflow %v", e)
	}
}
func TestCancelIsScopedAndCompletionInvalidatesPermissions(t *testing.T) {
	finished := make(chan struct{})
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		f := a.method("session/prompt")
		a.send(permission(`"s"`, "s"))
		a.send(permission(`"other"`, "other"))
		r := a.read()
		if string(r.ID) != `"s"` {
			t.Fatalf("wrong session cancelled: %s", r.ID)
		}
		a.method("session/cancel")
		r = a.read()
		if string(r.ID) != `"other"` {
			t.Fatalf("other session answer: %s", r.ID)
		}
		a.reply(f, `{"stopReason":"cancelled"}`)
		// A worker may finish while a permission card is still displayed.
		f = a.method("session/prompt")
		a.send(permission(`"stale"`, "s"))
		a.reply(f, `{"stopReason":"end_turn"}`)
		close(finished)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	done := make(chan error, 1)
	prompt := func() {
		_, e := c.Prompt(ctx, acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}})
		done <- e
	}
	go prompt()
	first, other := <-c.Permissions(), <-c.Permissions()
	if e := first.Answer(ctx, "not-offered"); e == nil {
		t.Fatal("unoffered option accepted")
	}
	if e := c.Cancel(ctx, "s"); e != nil {
		t.Fatal(e)
	}
	if e := other.Answer(ctx, "no"); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	go prompt()
	stale := <-c.Permissions()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	<-finished
	if e := stale.Answer(ctx, "yes"); !errors.Is(e, acp.ErrAnswered) {
		t.Fatalf("answered completed turn: %v", e)
	}
}
func TestFormElicitation(t *testing.T) {
	finished := make(chan struct{})
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		a.send(`{"jsonrpc":"2.0","id":null,"method":"elicitation/create","params":{"sessionId":"s","mode":"form","message":"Name","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}}}}}`)
		f := a.read()
		var r acp.CreateElicitationResponse
		if e := json.Unmarshal(f.Result, &r); e != nil || r.Decline == nil || string(f.ID) != "null" {
			t.Fatalf("form %s %v", f.Result, e)
		}
		a.send(`{"jsonrpc":"2.0","id":"url","method":"elicitation/create","params":{"mode":"url","url":"https://example.invalid"}}`)
		f = a.read()
		if f.Error == nil || f.Error.Code != -32602 {
			t.Fatalf("URL mode: %+v", f)
		}
		close(finished)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	select {
	case r := <-c.Requests():
		if r == nil {
			t.Fatalf("elicitation connection closed: %v", c.Err())
		}
		if string(r.ID) != "null" {
			t.Fatalf("elicitation ID %s", r.ID)
		}
		if e := r.Answer(ctx, acp.CreateElicitationResponse{Decline: &acp.CreateElicitationResponseDecline{}}); e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestOversizedAndTruncatedFrames(t *testing.T) {
	for _, payload := range []string{`{"jsonrpc":"2.0"}`, fmt.Sprintf("%0*d\n", rpc.MaxFrameBytes+1, 1)} {
		t.Run(fmt.Sprint(len(payload)), func(t *testing.T) {
			c, ctx := fixture(t, func(a agent) {
				a.initialize()
				a.method("session/prompt")
				_, _ = a.conn.Write([]byte(payload))
			})
			initialize(t, c, ctx)
			_, e := c.Prompt(ctx, acp.PromptRequest{SessionID: "s", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "work"}}}})
			if e == nil {
				t.Fatal("bad frame succeeded")
			}
		})
	}
}
