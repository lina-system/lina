// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"testing"

	"github.com/thisisjun786/lina/kit/acp"
)

func TestPromptSendBarrierOrdersImmediateCancel(t *testing.T) {
	cancelSeen, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	c, ctx := fixture(t, func(a agent) {
		a.initialize()
		prompt := a.method("session/prompt")
		a.method("session/cancel")
		close(cancelSeen)
		select {
		case <-release:
		case <-a.ctx.Done():
			return
		}
		a.reply(prompt, `{"stopReason":"cancelled"}`)
		<-a.ctx.Done()
	})
	initialize(t, c, ctx)
	result, err := c.StartPrompt(ctx, acp.PromptRequest{SessionID: "session", Prompt: []acp.ContentBlock{{Text: &acp.ContentBlockText{Text: "Work"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Cancel(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelSeen:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-result:
		t.Fatal("cancel acknowledgement completed the prompt")
	default:
	}
	// Release the fixture through a separate channel send so its cleanup remains
	// safe even when an assertion above aborts the test.
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case r := <-result:
		if r.Err != nil || r.Response.StopReason != acp.StopReasonCancelled {
			t.Fatal(r)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
