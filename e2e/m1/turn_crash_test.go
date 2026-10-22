// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

var turnCrashQueries = map[string]string{
	"claimed":     "SELECT count(*) FROM turn_runs",
	"prepared":    "SELECT count(*) FROM turn_runs WHERE prepared=1",
	"model_item":  "SELECT count(*) FROM turn_items",
	"tool_intent": "SELECT count(*) FROM turn_calls",
	"tool_result": "SELECT count(*) FROM turn_calls WHERE result IS NOT NULL",
	"completed":   "SELECT count(*) FROM turn_runs WHERE state='completed'",
}

type turnCommitBoundary struct {
	*canon.Store
	phase string
	t     *testing.T
}

func (db turnCommitBoundary) Write(ctx context.Context, f func(*canon.Tx) error) error {
	if err := db.Store.Write(ctx, f); err != nil {
		return err
	}
	query, ok := turnCrashQueries[db.phase]
	if !ok {
		return nil
	}
	var n int
	if err := db.Read(ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(ctx, query).Scan(&n) }); err != nil {
		return err
	}
	if n > 0 {
		acknowledgeCrash(db.t)
	}
	return nil
}

func TestM1TurnCrashBoundaries(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"claimed", "prepared", "model_item", "tool_intent", "tool_effect", "tool_result", "completed"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t, "")
			killAcceptanceChild(t, h, "TestM1TurnCrashChild", "LINA_M1_TURN_ROOT="+h.root, "LINA_M1_TURN_PHASE="+phase)
			db, _ := acceptanceStore(t, h.root, 2)
			store := turn.New(db)
			for range 2 {
				if err := store.Recover(h.ctx); err != nil {
					t.Fatal(err)
				}
			}
			record, err := store.Read(h.ctx, "turn")
			wantState, wantNotices := turn.Aborted, 1
			if phase == "completed" {
				wantState, wantNotices = turn.Completed, 0
			}
			if err != nil || record.State != wantState {
				t.Fatalf("recovery: %+v %v", record, err)
			}
			var notices int
			if err := db.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE parent_id='input' AND scaffold_kind='interruption'").Scan(&notices)
			}); err != nil || notices != wantNotices {
				t.Fatalf("notices=%d want=%d: %v", notices, wantNotices, err)
			}
			if err := store.Claim(h.ctx, "replay", "conversation", record.InboxID); !errors.Is(err, turn.ErrAccepted) {
				t.Fatalf("accepted input replayed: %v", err)
			}
			if phase == "tool_intent" || phase == "tool_effect" {
				if len(record.Results) != 1 || record.Results[0].Code != loop.Aborted {
					t.Fatalf("unresolved call: %+v", record.Results)
				}
			}
			if phase == "tool_result" || phase == "completed" {
				if len(record.Results) != 1 || record.Results[0].Code != loop.OK {
					t.Fatalf("lost result: %+v", record.Results)
				}
			}
			effect, err := os.ReadFile(filepath.Join(h.root, "effect"))
			if phase == "tool_effect" || phase == "tool_result" || phase == "completed" {
				if err != nil || string(effect) != "applied\n" {
					t.Fatalf("effect repeated or lost: %q %v", effect, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("effect ran before intent: %q %v", effect, err)
			}
		})
	}
}

func TestM1TurnCrashChild(t *testing.T) {
	root := os.Getenv("LINA_M1_TURN_ROOT")
	if root == "" {
		return
	}
	phase := os.Getenv("LINA_M1_TURN_PHASE")
	db, _ := acceptanceStore(t, root, 1)
	now := time.Now()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "input", ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "synthetic turn"})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "message"}, Body: []byte("synthetic turn")})
	if err != nil {
		t.Fatal(err)
	}
	round := 0
	call := provider.ToolCall{ID: "call", Name: "effect", Arguments: `{}`}
	_, err = turn.New(turnCommitBoundary{db, phase, t}).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: receipt.ID, InputEvent: "input", Loop: loop.Config{
		Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"effect"}`)}},
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			round++
			if round == 1 {
				return &scenarioRunStream{events: []provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"function_call","call_id":"call","name":"effect","arguments":"{}"}`)}, {Kind: provider.ToolCallReady, Call: &call}, {Kind: provider.Terminal}}}, nil
			}
			return &scenarioRunStream{events: []provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic completed reply"}]}`)}, {Kind: provider.Terminal}}}, nil
		},
		Tools: map[string]loop.Tool{"effect": {Validate: func(provider.ToolCall) (string, error) { return "synthetic", nil }, Execute: func(_ context.Context, call provider.ToolCall) loop.Result {
			file, err := os.OpenFile(filepath.Join(root, "effect"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			_, writeErr := file.WriteString("applied\n")
			err = errors.Join(writeErr, file.Sync(), file.Close())
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			if phase == "tool_effect" {
				acknowledgeCrash(t)
			}
			return loop.Result{ID: call.ID, Code: loop.OK, Output: json.RawMessage(`{}`)}
		}}},
	}})
	t.Fatalf("crash boundary not reached: %v", err)
}
