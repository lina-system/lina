// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"reflect"
	"testing"
)

func TestCapabilitiesRecordedBeforeEveryTurnRequest(t *testing.T) {
	db, _ := database(t)
	p := preparation()
	p.Digest = assemble.Digest
	p.Context = func(context.Context, turn.ModelPolicy) (turn.PreparedRequest, error) {
		r, report := assemble.Assemble(assemble.Input{MainID: "main", ConversationID: "conversation", AgentID: "agent", PersonaRevision: "v1", Model: "model", Current: json.RawMessage(`{"role":"user","content":"input"}`), MaxTokens: 100}, func(r provider.Request) int { return len(r.Items) })
		if report.Failure != "" {
			t.Fatal(report)
		}
		return turn.PreparedRequest{Request: r, Assembly: turn.Assembly{AgentID: "agent", PersonaRevision: "v1"}, MaxTokens: 100, Counter: func(r provider.Request) int { return len(r.Items) }}, nil
	}
	caps := provider.CapabilitySet{provider.CacheIdentityCapability, provider.CacheMarksCapability, provider.CacheLifetime1hCapability}
	p.SelectCapabilities = func(r provider.Request) (provider.Request, error) {
		var err error
		r.Capabilities, err = assemble.SelectCapabilities(r, caps, caps)
		return r, err
	}
	var saved []turn.RequestEvidence
	p.RecordRequest = func(_ context.Context, e turn.RequestEvidence) error { saved = append(saved, e); return nil }
	calls := 0
	_, err := turn.New(db).Run(t.Context(), turn.Options{ID: "cap-turn", Conversation: "conversation", InboxID: admit(t, db), Preparation: p, Loop: loop.Config{
		Tools: map[string]loop.Tool{"read": {Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
			return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"data"`)}
		}}},
		Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
			calls++
			if len(saved) != calls {
				t.Fatal("sent before evidence commit")
			}
			e := saved[calls-1]
			if e.Hash != assemble.Digest(r) || e.Capabilities == nil || !reflect.DeepEqual(*e.Capabilities, r.Capabilities) || !reflect.DeepEqual(e.Layout, r.Layout) {
				t.Fatal("wrong recorded values")
			}
			if calls == 1 {
				return reviewResponse(provider.ToolCall{ID: "read", Name: "read", Arguments: "{}"}), nil
			}
			if r.Layout.Newest == nil || r.Layout.Newest.Items != len(r.Items) || len(e.Capabilities.CacheMarks) != 3 {
				t.Fatal("follow-up mark missing")
			}
			return reviewResponse(), nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if saved[0].Layout.Newest != nil || len(saved[0].Capabilities.CacheMarks) != 2 {
		t.Fatal("follow-up mutated prior evidence")
	}
	var legacy turn.RequestEvidence
	if err := json.Unmarshal([]byte(`{"TurnID":"old","Hash":"old","Round":0,"Class":1,"Assembly":{"AgentID":"a"}}`), &legacy); err != nil || legacy.Layout != nil || legacy.Capabilities != nil || legacy.CacheIdentity != "" {
		t.Fatal("legacy evidence incompatible")
	}
}
