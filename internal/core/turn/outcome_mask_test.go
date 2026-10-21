// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestTurnOutcomeMasksOriginalInputAndNestedToolData(t *testing.T) {
	db, _ := database(t)
	id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "message"}, Body: []byte(`password="synthetic-input"`)})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := budgetConfig(t, func(request int) []provider.ToolCall {
		if request == 1 {
			return []provider.ToolCall{{ID: "read", Name: "read", Arguments: "{}"}}
		}
		return nil
	})
	tool := cfg.Tools["read"]
	tool.Execute = func(context.Context, provider.ToolCall) loop.Result {
		return loop.Result{Code: loop.OK, Output: json.RawMessage(`{"text":"The password=\"synthetic-output\" is set."}`)}
	}
	cfg.Tools["read"] = tool
	store := turn.New(db)
	if _, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: id.ID, Loop: cfg}); err != nil {
		t.Fatal(err)
	}
	o, err := store.Outcome(t.Context(), "turn")
	data, _ := json.Marshal(o)
	if err != nil || strings.Contains(string(data), "synthetic-input") || strings.Contains(string(data), "synthetic-output") {
		t.Fatalf("outcome retained credential data: %s %v", data, err)
	}
}
