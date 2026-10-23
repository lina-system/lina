// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type handoffRuntimeAdapter struct {
	provider.Adapter
	target string
}

func (h handoffRuntimeAdapter) Class() provider.Class { return provider.AgentRuntime }
func (h handoffRuntimeAdapter) Stream(ctx context.Context, _ lease.Lease, req provider.Request) (provider.Stream, error) {
	if req.Bridge == nil {
		return nil, errors.New("exchange runtime has no tool bridge")
	}
	raw, _ := json.Marshal(map[string]string{"id": h.target})
	got, err := req.Bridge.Call(ctx, provider.ToolCall{ID: "runtime-read", Name: "read_record", Arguments: string(raw)})
	if err != nil {
		return nil, err
	}
	var result loop.Result
	if json.Unmarshal(got.Output, &result) != nil || result.Code != loop.OK {
		return nil, errors.New("runtime record read failed")
	}
	return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Named record read."}]}`)}, {Kind: provider.Terminal}}}, nil
}

type handoffRuntimeStream struct{ events []provider.Event }

func (s *handoffRuntimeStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*handoffRuntimeStream) Meta() provider.Meta { return provider.Meta{Class: provider.AgentRuntime} }
func (*handoffRuntimeStream) Close() error        { return nil }

func exchangeRuntimeFixture(t *testing.T) (*conversationRuntime, exchange.Receipt, string) {
	t.Helper()
	r := controlRuntime(t)
	// This scenario drives one receiver explicitly, without scheduler races.
	r.exchangeStop()
	<-r.exchangeDone
	bindings := map[string]loop.Tool{}
	if err := r.prepareRecordTools("seed", "", bindings); err != nil {
		t.Fatal(err)
	}
	created := bindings["write_record"].Execute(t.Context(), provider.ToolCall{ID: "seed", Name: "write_record", Arguments: `{"id":"","revision":"","kind":"memo","title":"Draft","note":"Read this record"}`})
	var doc struct{ ID string }
	if created.Code != loop.OK || json.Unmarshal(created.Output, &doc) != nil {
		t.Fatalf("seed: %+v", created)
	}
	peer, err := r.agents.Create(t.Context(), agents.Create{Name: "Peer", PersonID: r.person})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	receipt, err := r.exchangeStore().Start(t.Context(), exchange.Request{DeliveryID: "runtime-handoff", From: r.agent, To: peer.ID, HandOff: exchange.HandOff{Subject: "Read draft", Wanted: "Read the named draft", OutOfScope: "No edits", Done: "Draft read", Check: "source", Deadline: now.Add(time.Hour)}, Origin: &exchange.Origin{Person: r.person, Conversation: r.conversation, CheckBack: true, NextCheck: now.Add(time.Hour), Artifact: exchange.Artifact{Kind: "wiki", ID: "record:" + doc.ID}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	return r, receipt, doc.ID
}

func TestExchangeAgentRuntimeExecutesNamedRecordThroughBridge(t *testing.T) {
	r, receipt, target := exchangeRuntimeFixture(t)
	r.route = &modelpath.Config{Adapter: handoffRuntimeAdapter{target: target}, Model: "fixture"}
	if err := r.receiveHandoff(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	saved, err := turn.New(r.db).Read(t.Context(), "exchange-turn:"+receipt.DeliveryID)
	if err != nil || saved.State != turn.Completed || len(saved.Results) != 1 || saved.Results[0].Code != loop.OK {
		t.Fatalf("unrecorded runtime result: %+v %v", saved, err)
	}
	status, err := r.exchangeStore().Status(t.Context(), receipt.ID, r.person, r.agent)
	if err != nil || status.Report == nil {
		t.Fatalf("missing receiver report: %+v %v", status, err)
	}
}

type exchangeMemoryAdapter struct {
	provider.Adapter
	r        *conversationRuntime
	target   string
	text     string
	change   func(context.Context) error
	rounds   int
	unfenced bool
}

func (*exchangeMemoryAdapter) Class() provider.Class { return provider.ModelAPI }
func (a *exchangeMemoryAdapter) Stream(_ context.Context, _ lease.Lease, req provider.Request) (provider.Stream, error) {
	a.rounds++
	if a.r.dispatchMu.TryLock() {
		a.unfenced = true
		a.r.dispatchMu.Unlock()
	}
	raw, _ := json.Marshal(req.Items)
	if !bytes.Contains(raw, []byte(a.text)) {
		return nil, errors.New("common memory missing from exchange fixture")
	}
	stream := &exchangeMemoryStream{}
	if a.rounds == 1 {
		args, _ := json.Marshal(map[string]string{"id": a.target})
		call := provider.ToolCall{ID: "read-before-change", Name: "read_record", Arguments: string(args)}
		item, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
		stream.events = []provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.ToolCallReady, Call: &call}, {Kind: provider.Terminal}}
		stream.change = a.change
	} else {
		stream.events = []provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Read complete."}]}`)}, {Kind: provider.Terminal}}
	}
	return stream, nil
}

type exchangeMemoryStream struct {
	handoffRuntimeStream
	change func(context.Context) error
}

func (s *exchangeMemoryStream) Meta() provider.Meta { return provider.Meta{Class: provider.ModelAPI} }
func (s *exchangeMemoryStream) Next(ctx context.Context) (provider.Event, error) {
	if s.change != nil {
		change := s.change
		s.change = nil
		// Commit after dispatch returns, before the follow-up request. No timing race.
		if err := change(ctx); err != nil {
			return provider.Event{}, err
		}
	}
	return s.handoffRuntimeStream.Next(ctx)
}

func TestExchangeRevalidatesCommonMemoryBeforeEachDispatch(t *testing.T) {
	for _, operation := range []string{"unchanged", "forget", "correct"} {
		t.Run(operation, func(t *testing.T) {
			r, receipt, target := exchangeRuntimeFixture(t)
			session := clientsrv.Session{PersonID: r.person}
			text := "Common orchard fixture"
			saved, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: str("remember"), AgentID: &r.agent, Holder: str("all"), Text: &text})
			if err != nil {
				t.Fatal(err)
			}
			adapter := &exchangeMemoryAdapter{r: r, target: target, text: text}
			if operation != "unchanged" {
				adapter.change = func(ctx context.Context) error {
					request := protocol.MemoryRequest{Operation: &operation, AgentID: &r.agent, ItemID: saved.ItemID}
					if operation == "correct" {
						request.Text = str("Common meadow fixture")
					}
					_, err := r.Memory(ctx, session, request)
					return err
				}
			}
			r.route = &modelpath.Config{Adapter: adapter, Model: "fixture"}
			err = r.receiveHandoff(t.Context(), receipt)
			if operation == "unchanged" {
				if err != nil || adapter.rounds != 2 {
					t.Fatalf("unchanged memory blocked: rounds=%d err=%v", adapter.rounds, err)
				}
			} else if !errors.Is(err, retract.ErrStaleSnapshot) || adapter.rounds != 1 {
				t.Fatalf("changed memory reached a follow-up: rounds=%d err=%v", adapter.rounds, err)
			}
			if adapter.unfenced {
				t.Error("exchange dispatch does not share the memory mutation fence")
			}
			state, readErr := turn.New(r.db).Read(t.Context(), "exchange-turn:"+receipt.DeliveryID)
			if readErr != nil || len(state.Results) != 1 || state.Results[0].Code != loop.OK {
				t.Fatalf("record tool did not run: %+v %v", state, readErr)
			}
		})
	}
}
