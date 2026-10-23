// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestConversationPreparesExpand(t *testing.T) {
	r, _ := turnRuntime(t)
	input, inboxID := admitTurnInput(t, r, "expand-source")
	s := turnState{runtime: r, input: input, turnID: "turn:" + input.ID, bindings: map[string]loop.Tool{}, filters: map[string]*conversationReplyStream{}, questions: &questionTurn{}}
	if err := turn.New(r.db).Claim(t.Context(), s.turnID, r.conversation, inboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepare(t.Context(), turn.ModelPolicy{Model: "turn-fixture"}); err != nil {
		t.Fatal(err)
	}
	tool, ok := s.bindings["expand"]
	if !ok {
		t.Fatal("normal conversation preparation did not bind expand")
	}
	args, _ := json.Marshal(map[string]string{"id": "event:" + input.ID})
	result := tool.Execute(t.Context(), provider.ToolCall{ID: "expand", Name: "expand", Arguments: string(args)})
	var page compaction.ExpansionPage
	if result.Code != loop.OK || json.Unmarshal(result.Output, &page) != nil || len(page.Events) != 1 || page.Events[0].Body != input.Body.Text() {
		t.Fatalf("expand: %+v", result)
	}
}

func TestExpandRespectsChannelMemoryScope(t *testing.T) {
	r, _ := turnRuntime(t)
	bindings := map[string]loop.Tool{}
	declarations, err := r.prepareExpandTools("turn", nil, bindings)
	if err != nil || len(declarations) != 1 {
		t.Fatalf("prepare: %v %v", declarations, err)
	}
	tool := bindings["expand"]
	r.channelBinding = &channels.Binding{Scope: channels.Scope{Tools: true, Memory: false}}
	args := `{"id":"event:source"}`
	if result := tool.Execute(t.Context(), provider.ToolCall{ID: "call", Name: "expand", Arguments: args}); result.Code != loop.Denied {
		t.Fatalf("restricted channel: %+v", result)
	}
	bindings = map[string]loop.Tool{}
	declarations, err = r.prepareExpandTools("turn", nil, bindings)
	if err != nil || len(declarations) != 0 || len(bindings) != 0 {
		t.Fatalf("restricted tool exposed: %v %v", declarations, err)
	}
}
