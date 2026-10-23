// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/moirai"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationSessionCompactsBeforeToolFollowup(t *testing.T) {
	r, p := turnRuntime(t)
	queuedContextEvent(t, r, "archive", "archive-marker "+strings.Repeat("archive ", 4000), guard.EntryOwner, "")
	queuedContextEvent(t, r, "archive-reply", "Archived.", guard.EntryAgent, "")
	input, inbox := admitTurnInput(t, r, "continue-current-work")
	reasoning, _ := json.Marshal(map[string]string{"type": "reasoning", "encrypted_content": strings.Repeat("opaque", 6000)})
	call := provider.ToolCall{ID: "index", Name: "skill_index", Arguments: "{}"}
	callItem := json.RawMessage(`{"type":"function_call","call_id":"index","name":"skill_index","arguments":"{}"}`)
	summary, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "Archived.", Values: "archive-marker", Omitted: "repeated archive"})
	p.steps = []turnStep{
		{events: []provider.Event{{Kind: provider.ItemDone, Index: 0, Item: reasoning}, {Kind: provider.ItemDone, Index: 1, Item: callItem}, {Kind: provider.ToolCallReady, Index: 1, Call: &call}, {Kind: provider.Terminal}}},
		{before: func(req provider.Request) {
			if req.Purpose != provider.Auxiliary {
				t.Fatalf("oversized tool follow-up bypassed compaction: purpose=%s size=%d", req.Purpose, conversationRequestSize(req))
			}
			raw, _ := json.Marshal(req.Items)
			if bytes.Contains(raw, []byte("opaque")) || bytes.Contains(raw, []byte(input.ID)) {
				t.Fatal("summary consumed active turn")
			}
		}, events: turnText(string(summary))},
		{before: func(req provider.Request) {
			raw, _ := json.Marshal(req.Items)
			if !bytes.Contains(raw, []byte("specific_values")) || bytes.Contains(raw, []byte("archive archive")) {
				t.Fatal("follow-up did not replace summarized history")
			}
			current, _ := assemble.History([]ledger.Event{input}, r.conversation, r.agent)
			if req.Layout == nil || !req.Layout.Valid(req) || !bytes.Equal(req.Items[req.Layout.CurrentInput.Items-1], current[0]) || req.Layout.PreviousInput != nil || req.Layout.Newest == nil || req.Layout.Newest.Items != len(req.Items) {
				t.Fatalf("compaction lost provider cache boundaries: layout=%+v items=%d", req.Layout, len(req.Items))
			}
			count := 0
			found := false
			for _, item := range req.Items {
				if bytes.Equal(item, current[0]) {
					count++
				}
				if bytes.Equal(item, reasoning) {
					found = true
				}
			}
			if !found || count != 1 {
				t.Fatal("active reasoning or current input changed")
			}
		}, events: turnText("Finished.")},
	}
	if err := r.run(t.Context(), &loop.Inputs{}, input, inbox); err != nil {
		for _, req := range p.requests {
			t.Logf("request purpose=%s size=%d", req.Purpose, conversationRequestSize(req))
		}
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("cause: %T %v", cause, cause)
		}
		t.Fatal(err)
	}
	assertCompactionRequests(t, p, 3)
	frontier, err := compaction.New(r.db).Frontier(t.Context(), r.conversation)
	if err != nil || frontier.Root == "" {
		t.Fatalf("no published summary: %+v %v", frontier, err)
	}
	ids, _, err := compaction.New(r.db).ExpandEvents(t.Context(), r.agent, frontier.Root, 0, 100)
	if err != nil || len(ids) != 2 || ids[0] != "archive" || ids[1] != "archive-reply" {
		t.Fatalf("coverage=%v %v", ids, err)
	}
}

func TestConversationQueuesSummaryOnlyAfterDelivery(t *testing.T) {
	for _, condense := range []bool{false, true} {
		t.Run(fmt.Sprintf("condense=%v", condense), func(t *testing.T) {
			r, p := turnRuntime(t)
			r.route.Adapter = usageTurnProvider{p}
			queuedContextEvent(t, r, "archive", strings.Repeat("archive ", 4000), guard.EntryOwner, "")
			queuedContextEvent(t, r, "archive-reply", "Archived.", guard.EntryAgent, "")
			input, inbox := admitTurnInput(t, r, "finish-current-work")
			count := func(table string) int {
				t.Helper()
				var n int
				err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
				})
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			p.steps = []turnStep{{before: func(req provider.Request) {
				if count("moirai_tasks") != 0 {
					t.Fatal("summary prepared before answer delivery")
				}
			}, events: turnText(strings.Repeat("Archive complete. ", 1100))}}
			if err := r.run(t.Context(), &loop.Inputs{}, input, inbox); err != nil {
				t.Fatal(err)
			}
			if count("moirai_tasks") == 0 {
				t.Fatal("completed request crossed the threshold but no summary preparation was queued")
			}
			if count("sum_node") != 0 {
				t.Fatal("after-delivery preparation published a summary node")
			}
			assertTurnRequests(t, p, 1)
			summary, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "Archived.", Values: "42", Omitted: "repeated archive"})
			if condense {
				large, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "Archived.", Values: strings.Repeat("fact ", 5000), Omitted: "repeated archive"})
				p.steps = append(p.steps, turnStep{events: turnText(string(large))})
			}
			p.steps = append(p.steps, turnStep{events: turnText(string(summary))}, turnStep{events: turnText("Done.")})
			m := &r.summaryPreparation
			job, err := m.jobs.Policy(t.Context(), m.id)
			if err != nil {
				t.Fatal(err)
			}
			if err := (moirai.Worker{Core: m.jobs}).Step(t.Context(), jobapi.Work{Epoch: r.epoch, Job: job, Task: jobapi.Task{Sequence: 1}}); err != nil {
				t.Fatal(err)
			}
			if count("sum_node") != 0 {
				t.Fatal("worker published before foreground fitting")
			}
			var charged int
			if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT charged FROM budget_usage WHERE run=?", m.id).Scan(&charged)
			}); err != nil {
				t.Fatal(err)
			}
			calls := 1
			if condense {
				calls = 2
			}
			if charged != 168*calls {
				t.Fatalf("summary usage charged=%d, want reported input+output %d", charged, 168*calls)
			}
			next, receipt := admitTurnInput(t, r, "next-work")
			if err := r.run(t.Context(), &loop.Inputs{}, next, receipt); err != nil {
				t.Fatal(err)
			}
			assertCompactionRequests(t, p, 2+calls)
			if count("sum_node") != 1 {
				t.Fatal("prepared summary was not published by next turn")
			}
		})
	}
}

type budgetTurnProvider struct{ *turnProvider }

func (p budgetTurnProvider) RequestBudget(model provider.ModelRef) (int, func(provider.Request) int) {
	return 100000, conversationRequestSize
}
func TestConversationUsesAdapterContextBudget(t *testing.T) {
	r, p := turnRuntime(t)
	r.route.Adapter = budgetTurnProvider{p}
	queuedContextEvent(t, r, "archive", strings.Repeat("archive ", 4000), guard.EntryOwner, "")
	queuedContextEvent(t, r, "reply", "Archived.", guard.EntryAgent, "")
	input, inbox := admitTurnInput(t, r, "continue")
	summary, _ := json.Marshal(compaction.Text{Requests: "none", Topics: "archive", Actions: "Archived.", Values: "42", Omitted: "repeated archive"})
	p.steps = []turnStep{{before: func(req provider.Request) {
		if req.Purpose != provider.Auxiliary {
			t.Fatal("Core ignored the adapter context window")
		}
	}, events: turnText(string(summary))}, {events: turnText("Done.")}}
	if err := r.run(t.Context(), &loop.Inputs{}, input, inbox); err != nil {
		t.Fatal(err)
	}
	assertCompactionRequests(t, p, 2)
}

func assertCompactionRequests(t *testing.T, p *turnProvider, n int) {
	t.Helper()
	if len(p.requests) != n {
		t.Fatalf("requests=%d want=%d", len(p.requests), n)
	}
	for _, stream := range p.streams {
		if !stream.closed {
			t.Fatal("stream left open")
		}
	}
}

func TestConversationOversizeRecordsContinuationAndSizes(t *testing.T) {
	r, p := turnRuntime(t)
	input, inbox := admitTurnInput(t, r, "continue")
	reasoning, _ := json.Marshal(map[string]string{"type": "reasoning", "encrypted_content": strings.Repeat("opaque", 14000)})
	call := provider.ToolCall{ID: "index", Name: "skill_index", Arguments: "{}"}
	p.steps = []turnStep{{events: []provider.Event{{Kind: provider.ItemDone, Index: 0, Item: reasoning}, {Kind: provider.ItemDone, Index: 1, Item: json.RawMessage(`{"type":"function_call","call_id":"index","name":"skill_index","arguments":"{}"}`)}, {Kind: provider.ToolCallReady, Index: 1, Call: &call}, {Kind: provider.Terminal}}}}
	if err := r.run(t.Context(), &loop.Inputs{}, input, inbox); err == nil {
		t.Fatal("oversized active turn dispatched")
	}
	var continuation, sizes int
	err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE json_extract(body,'$.kind')='continuation'),count(*) FILTER(WHERE json_extract(body,'$.kind')='continuation_sizes') FROM conv_event WHERE scaffold_kind='compaction_recorded'`).Scan(&continuation, &sizes)
	})
	if err != nil || continuation != 1 || sizes != 1 {
		t.Fatalf("continuations=%d sizes=%d err=%v", continuation, sizes, err)
	}
	assertCompactionRequests(t, p, 1)
	record, err := turn.New(r.db).Read(t.Context(), "turn:"+input.ID)
	if err != nil || record.State != turn.Failed || len(record.Items) != 2 || !bytes.Equal(record.Items[0], reasoning) {
		t.Fatalf("ended journal changed: state=%s items=%d err=%v", record.State, len(record.Items), err)
	}
	diagnostic, err := r.journal().Get(t.Context(), "continuation-sizes:turn:"+input.ID)
	if err != nil {
		t.Fatal(err)
	}
	var measured struct {
		Sizes compaction.Sizes `json:"sizes"`
	}
	if err := json.Unmarshal([]byte(diagnostic.Body.Text()), &measured); err != nil {
		t.Fatal(err)
	}
	if measured.Sizes.Total < measured.Sizes.Threshold || measured.Sizes.Window != 128000 || measured.Sizes.Active < len(reasoning) {
		t.Fatalf("invalid breakdown: %+v", measured.Sizes)
	}
	capture := func() protocol.Result {
		t.Helper()
		snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		for _, item := range snapshot.Snapshot.Items {
			if item.Kind == protocol.KindResult {
				var result protocol.Result
				if err := json.Unmarshal(item.Data, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
		}
		t.Fatal("size Details were not delivered")
		return protocol.Result{}
	}
	live := capture()
	if live.Text == nil || !strings.Contains(*live.Text, fmt.Sprintf("Request: %d", measured.Sizes.Total)) {
		t.Fatal("Details differ from recorded measurement")
	}
	if err := r.seedHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored := capture()
	if restored.Text == nil || restored.ResultID != live.ResultID || *restored.Text != *live.Text {
		t.Fatal("restored Details changed")
	}
	t.Logf("Diagnostic capture: %s — %s\n%s", live.Title, live.Summary, *live.Text)
}

type usageTurnProvider struct{ *turnProvider }

func (p usageTurnProvider) Stream(ctx context.Context, l lease.Lease, request provider.Request) (provider.Stream, error) {
	stream, err := p.turnProvider.Stream(ctx, l, request)
	if err != nil || request.Purpose != provider.Auxiliary {
		return stream, err
	}
	input, output := int64(123), int64(45)
	return usageTurnStream{Stream: stream, usage: provider.Usage{Input: &input, Output: &output}}, nil
}

type usageTurnStream struct {
	provider.Stream
	usage provider.Usage
}

func (s usageTurnStream) Meta() provider.Meta { m := s.Stream.Meta(); m.Usage = s.usage; return m }

func TestDiscardedSummaryPreparationHasNoModelCharge(t *testing.T) {
	r, p := turnRuntime(t)
	if err := r.ensureSummaryPreparation(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := r.summaryState()
	job, err := m.jobs.Policy(t.Context(), m.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.jobs.Enqueue(t.Context(), m.id, jobapi.Task{Sequence: 1, Input: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := (moirai.Worker{Core: m.jobs}).Step(t.Context(), jobapi.Work{Epoch: r.epoch, Job: job, Task: jobapi.Task{Sequence: 1}}); err != nil {
		t.Fatal(err)
	}
	var charged int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT charged FROM budget_usage WHERE run=?", m.id).Scan(&charged)
	}); err != nil {
		t.Fatal(err)
	}
	if charged != 0 || len(p.requests) != 0 {
		t.Fatalf("discard charged=%d requests=%d", charged, len(p.requests))
	}
}
