// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
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

func TestExchangeAgentRuntimeExecutesNamedRecordThroughBridge(t *testing.T) {
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
	r.route = &modelpath.Config{Adapter: handoffRuntimeAdapter{target: doc.ID}, Model: "fixture"}
	now := time.Now()
	receipt, err := r.exchangeStore().Start(t.Context(), exchange.Request{DeliveryID: "runtime-handoff", From: r.agent, To: peer.ID, HandOff: exchange.HandOff{Subject: "Read draft", Wanted: "Read the named draft", OutOfScope: "No edits", Done: "Draft read", Check: "source", Deadline: now.Add(time.Hour)}, Origin: &exchange.Origin{Person: r.person, Conversation: r.conversation, CheckBack: true, NextCheck: now.Add(time.Hour), Artifact: exchange.Artifact{Kind: "wiki", ID: "record:" + doc.ID}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.receiveHandoff(t.Context(), receipt); err != nil {
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
