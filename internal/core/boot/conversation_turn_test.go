// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestTurnStateObservationCompletionPublication(t *testing.T) {
	r, _ := turnRuntime(t)
	input, inboxID := admitTurnInput(t, r, "state-input")
	s := turnState{
		runtime: r, input: input, turnID: "turn:" + input.ID,
		bindings: map[string]loop.Tool{}, filters: map[string]*conversationReplyStream{}, questions: &questionTurn{},
	}
	if err := turn.New(r.db).Claim(t.Context(), s.turnID, r.conversation, inboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for round, text := range []string{"First reply.", "Second reply."} {
		s.round = round + 1
		key := fmt.Sprintf("%s:%d:7", s.turnID, s.round)
		keys = append(keys, key)
		for _, event := range turnText(text) {
			event.Index = 7
			if err := s.observe(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		// Each response has its own filter even when output indices repeat.
		if filter := s.filters[key]; filter == nil || filter.sent != text {
			t.Fatalf("stream %s = %+v", key, filter)
		}
		if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
			var saved string
			if err := tx.QueryRowContext(t.Context(), `SELECT body FROM turn_partials WHERE turn_id=? AND round=? AND ordinal=?`, s.turnID, round, 7).Scan(&saved); err != nil {
				return err
			}
			if saved != text {
				t.Errorf("checkpoint round %d = %q, want %q", round, saved, text)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.journal().Get(t.Context(), key); err == nil {
			t.Fatalf("provisional item %s admitted before completion", key)
		}
	}
	start, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var streams []string
	for _, item := range start.Snapshot.Items {
		if item.Kind == protocol.KindOutputDelta {
			streams = append(streams, item.Key)
		}
	}
	if !reflect.DeepEqual(streams, keys) {
		t.Fatalf("visible stream keys = %v, want %v", streams, keys)
	}
	if err := s.complete(t.Context(), r.db); err != nil {
		t.Fatal(err)
	}
	// Completion admits replies and their checks, but does not publish them.
	for _, key := range keys {
		item, err := r.journal().Get(t.Context(), key)
		if err != nil || item.ParentID != input.ID || item.SpeakerKind != guard.Agent {
			t.Fatalf("completed reply %s: %+v, %v", key, item, err)
		}
	}
	var checks int
	for _, item := range s.saved {
		if item.ScaffoldKind == "reply_check" {
			checks++
		}
	}
	if checks != len(keys) || len(s.saved) != 2*len(keys) {
		t.Fatalf("completion batch = %+v", s.saved)
	}
	assertTurnPublished(t, r, input.ID)
	if err := s.publishOutcome(t.Context(), loop.Outcome{}, nil); err != nil {
		t.Fatal(err)
	}
	assertTurnPublished(t, r, append([]string{input.ID}, keys...)...)
}

func TestConversationTurnText(t *testing.T) {
	r, p := turnRuntime(t)
	p.steps = []turnStep{{events: turnText("Fixture reply.")}}
	input, inboxID := admitTurnInput(t, r, "text-input")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inboxID); err != nil {
		t.Fatal(err)
	}
	record := assertTurnRecord(t, r, input, inboxID, turn.Completed, 1)
	if len(record.Items) != 1 || len(record.Results) != 0 || !reflect.DeepEqual(record.Items[0], p.steps[0].events[1].Item) {
		t.Fatalf("text turn evidence: %+v", record)
	}
	reply, err := r.journal().Get(t.Context(), "turn:text-input:1:0")
	if err != nil || reply.ParentID != input.ID || reply.SpeakerKind != guard.Agent || reply.AgentID != r.agent || reply.Body.Text() != "Fixture reply." {
		t.Fatalf("reply provenance: %+v, %v", reply, err)
	}
	assertTurnPublished(t, r, input.ID, reply.ID)
	assertTurnRequests(t, p, 1)
}

func TestConversationStopPublishesOutcomeAfterCancellation(t *testing.T) {
	r, p := turnRuntime(t)
	input, inboxID := admitTurnInput(t, r, "stopped-input")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	p.steps = []turnStep{{before: func(provider.Request) {
		cancel(turn.Interrupt{Issuer: turn.Person})
	}}}
	if err := r.run(ctx, &loop.Inputs{}, input, inboxID); err == nil {
		t.Fatal("cancelled turn returned success")
	}
	stopped, err := turn.New(r.db).Stopped(t.Context(), input.ID)
	if err != nil || stopped.NoticeID == "" {
		t.Fatalf("durable stop outcome: %+v, %v", stopped, err)
	}
	assertTurnPublished(t, r, input.ID, stopped.NoticeID)
}

func TestConversationTurnTools(t *testing.T) {
	r, p := turnRuntime(t)
	calls := []provider.ToolCall{{ID: "skills", Name: "skill_index", Arguments: `{}`}, {ID: "plans", Name: "plan_index", Arguments: `{}`}}
	var events []provider.Event
	for i, call := range calls {
		raw, err := json.Marshal(map[string]string{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, provider.Event{Kind: provider.ItemDone, Index: int64(i), Item: raw}, provider.Event{Kind: provider.ToolCallReady, Index: int64(i), Call: &calls[i]})
	}
	events = append(events, provider.Event{Kind: provider.Terminal})
	p.steps = []turnStep{{events: events}, {events: turnText("Tools finished.")}}
	input, inboxID := admitTurnInput(t, r, "tool-input")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inboxID); err != nil {
		t.Fatal(err)
	}
	record := assertTurnRecord(t, r, input, inboxID, turn.Completed, 2)
	if len(record.Items) != 3 || len(record.Results) != len(calls) {
		t.Fatalf("tool turn evidence: %+v", record)
	}
	assertTurnRequests(t, p, 2)
	var outputs []loop.Result
	for _, raw := range p.requests[1].Items {
		var item struct{ Type, Output string }
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if item.Type == "function_call_output" {
			var result loop.Result
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Fatal(err)
			}
			outputs = append(outputs, result)
		}
	}
	if !reflect.DeepEqual(outputs, record.Results) {
		t.Fatalf("model tool results differ from ordered durable receipts: %v / %v", outputs, record.Results)
	}
	for i, result := range record.Results {
		if result.ID != calls[i].ID || result.Code != loop.OK || len(result.Output) == 0 {
			t.Fatalf("tool receipt %d: %+v", i, result)
		}
	}
	assertTurnPublished(t, r, input.ID, "turn:tool-input:2:0")
}

func TestConversationTurnSteering(t *testing.T) {
	for _, afterFailure := range []bool{false, true} {
		name := "next-round"
		if afterFailure {
			name = "follow-up-after-failure"
		}
		t.Run(name, func(t *testing.T) {
			r, p := turnRuntime(t)
			var inputs *loop.Inputs
			input, inboxID := admitTurnInput(t, r, "steering-input")
			var steering ledger.Event
			var steeringInbox int64
			first := turnStep{events: turnText("First reply."), before: func(provider.Request) {
				steering, steeringInbox = admitTurnInput(t, r, "steering-follow-up")
				steerTurn(t, r, steering, steeringInbox)
			}}
			failure := &provider.Failure{Kind: provider.Unavailable}
			if afterFailure {
				first.err = failure
			}
			p.steps = []turnStep{first, {events: turnText("Follow-up reply.")}}
			done, err := r.queueReaction(t.Context(), r.conversation, func(ctx context.Context, queue *loop.Inputs) error {
				inputs = queue
				return r.run(ctx, queue, input, inboxID)
			})
			if err != nil {
				t.Fatal(err)
			}
			err = awaitTurn(t, done)
			if afterFailure {
				if !errors.Is(err, failure) {
					t.Fatalf("provider failure = %v", err)
				}
				// A lane barrier joins the queued follow-up without polling or sleeping.
				done, err := r.queueReaction(t.Context(), r.conversation, func(context.Context, *loop.Inputs) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if err := awaitTurn(t, done); err != nil {
					t.Fatal(err)
				}
				assertTurnRecord(t, r, input, inboxID, turn.Failed, 1)
				assertTurnRecord(t, r, steering, steeringInbox, turn.Completed, 1)
				stopped, err := turn.New(r.db).Stopped(t.Context(), input.ID)
				if err != nil {
					t.Fatal(err)
				}
				assertTurnPublished(t, r, input.ID, steering.ID, stopped.NoticeID, "turn:steering-follow-up:1:0")
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertTurnRecord(t, r, input, inboxID, turn.Completed, 2)
				assertTurnPublished(t, r, input.ID, steering.ID, "turn:steering-input:1:0", "turn:steering-input:2:0")
			}
			assertTurnRequests(t, p, 2)
			var occurrences int
			for _, raw := range p.requests[1].Items {
				var item struct{ Role, Content string }
				if json.Unmarshal(raw, &item) == nil && item.Role == "user" && item.Content == steering.Body.Text() {
					occurrences++
				}
			}
			if occurrences != 1 || len(r.steering) != 0 || inputs.Push(provider.Item(`{}`)) {
				t.Fatalf("steering not consumed exactly once: occurrences=%d pending=%d", occurrences, len(r.steering))
			}
		})
	}
}

func TestConversationTurnProviderFailure(t *testing.T) {
	r, p := turnRuntime(t)
	failure := &provider.Failure{Kind: provider.Unavailable}
	p.steps = []turnStep{{err: failure}}
	input, inboxID := admitTurnInput(t, r, "failed-input")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inboxID); !errors.Is(err, failure) {
		t.Fatalf("provider failure = %v", err)
	}
	record := assertTurnRecord(t, r, input, inboxID, turn.Failed, 1)
	if len(record.Items) != 0 || len(record.Results) != 0 {
		t.Fatalf("failure manufactured output: %+v", record)
	}
	stopped, err := turn.New(r.db).Stopped(t.Context(), input.ID)
	if err != nil || stopped.NoticeID == "" {
		t.Fatalf("durable failure notice: %+v, %v", stopped, err)
	}
	notice, err := r.journal().Get(t.Context(), stopped.NoticeID)
	if err != nil || notice.SpeakerKind != guard.System || notice.ParentID != input.ID || notice.ScaffoldKind != "failure" {
		t.Fatalf("failure provenance: %+v, %v", notice, err)
	}
	assertTurnPublished(t, r, input.ID, notice.ID)
	assertTurnRequests(t, p, 1)
}

func TestFailedWorkerReactionKeepsDeclarationsWithoutExecution(t *testing.T) {
	r, _ := turnRuntime(t)
	var first provider.Request
	for _, failure := range []bool{false, true} {
		input, inboxID := admitTurnInput(t, r, fmt.Sprintf("reaction-%t", failure))
		if failure {
			input.ScaffoldKind = "worker_completion"
		} // Unparseable completion is fail-closed.
		s := turnState{runtime: r, input: input, turnID: "turn:" + input.ID, bindings: map[string]loop.Tool{}, filters: map[string]*conversationReplyStream{}, questions: &questionTurn{}}
		if err := turn.New(r.db).Claim(t.Context(), s.turnID, r.conversation, inboxID); err != nil {
			t.Fatal(err)
		}
		prepared, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"})
		if err != nil {
			t.Fatal(err)
		}
		if !failure {
			first = prepared.Request
			continue
		}
		if len(first.Tools) == 0 || !reflect.DeepEqual(first.Tools, prepared.Request.Tools) || first.Instructions != prepared.Request.Instructions {
			t.Error("failure reaction changed fixed instructions or tool declarations")
		}
		if len(s.bindings) != 0 {
			t.Fatal("failure reaction can execute tools")
		}
	}
}
