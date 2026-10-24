// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestChannelRunInboxKeepsReceiptBesideTurn(t *testing.T) {
	id, ok, err := channelRunInbox(runs.Origin{Turn: "turn:app-exec", ChannelReceipt: "channel-input:9"})
	if err != nil || !ok || id != 9 {
		t.Fatalf("selected receipt = %d %v %v", id, ok, err)
	}
	id, ok, err = channelRunInbox(runs.Origin{Turn: "turn:channel-input:4"})
	if err != nil || !ok || id != 4 {
		t.Fatalf("channel turn = %d %v %v", id, ok, err)
	}
	_, ok, err = channelRunInbox(runs.Origin{Turn: "turn:app-exec"})
	if err != nil || ok {
		t.Fatalf("app turn looked like a channel receipt: %v %v", ok, err)
	}
	if _, _, err = channelRunInbox(runs.Origin{ChannelReceipt: "turn:channel-input:1"}); err == nil {
		t.Fatal("malformed receipt was accepted")
	}
}

func TestRecoveredExplicitStartConfirmsReceipt(t *testing.T) {
	r := controlRuntime(t)
	s, err := runs.New(runs.Config{Epoch: r.epoch, DB: r.db, Assets: r.assets, WorkDir: filepath.Join(r.root, "work"), Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("recovery replayed work")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	input, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: "recovery-start"}, Body: []byte("inspect")})
	if err != nil {
		t.Fatal(err)
	}
	turns := turn.New(r.db)
	if err = turns.Claim(t.Context(), "recover-start", r.conversation, input.ID); err != nil {
		t.Fatal(err)
	}
	b := runs.Brief{Goal: "inspect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	args, _ := json.Marshal(b)
	call := provider.ToolCall{ID: "start-call", Name: runs.StartToolName, Arguments: string(args)}
	other := provider.ToolCall{ID: "not-started", Name: runs.StartToolName, Arguments: string(args)}
	if err = turns.Journal("recover-start").Calls(t.Context(), 0, []provider.ToolCall{call, other}); err != nil {
		t.Fatal(err)
	}
	result := s.StartTool(r.runOrigin("recover-start")).Execute(t.Context(), call)
	if result.Code != loop.OK {
		t.Fatal(result)
	}
	// Simulate death after run admission and before the caller's result commit.
	if err = recoverConversationTurns(t.Context(), r.db); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var receipt runs.Receipt
	if err = json.Unmarshal(result.Output, &receipt); err != nil {
		t.Fatal(err)
	}
	record, err := s.Get(t.Context(), receipt.ID)
	if err != nil || record.Retries != 0 || record.Released {
		t.Fatalf("unstarted receipt consumed retry or executed: %+v %v", record, err)
	}
	got, err := turns.Read(t.Context(), "recover-start")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 || got.Results[0].Code != loop.OK || string(got.Results[0].Output) != string(result.Output) || got.Results[1].Code != loop.Aborted {
		t.Fatalf("recovered receipt=%+v; want committed success and unstarted abort", got.Results)
	}
}
