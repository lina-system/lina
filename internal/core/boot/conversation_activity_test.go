// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationToolPhasesAndStopping(t *testing.T) {
	topics, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer topics.Close()
	runtime := &conversationRuntime{topics: topics, conversation: "test", agent: "agent"}
	if err := runtime.setActive(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	snapshot := func() protocol.TurnState {
		t.Helper()
		start, sub, err := topics.Subscribe("conversation:test", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		if len(start.Snapshot.Items) != 1 {
			t.Fatalf("activity entered message window: %+v", start.Snapshot.Items)
		}
		var phase protocol.TurnState
		if err := json.Unmarshal(start.Snapshot.Items[0].Data, &phase); err != nil {
			t.Fatal(err)
		}
		return phase
	}
	activity := func(id, name string, started bool, code loop.Code) {
		t.Helper()
		if err := runtime.toolActivity(t.Context(), turn.ToolActivity{CallID: id, ToolName: name, Started: started, Code: code, Duration: time.Millisecond}); err != nil {
			t.Fatal(err)
		}
	}
	activity("one", "read_skill", true, "")
	activity("two", "skill_index", true, "")
	p := snapshot()
	if p.Phase == nil || *p.Phase != protocol.TurnPhaseTool || p.ToolName == nil || *p.ToolName != "Listing skills" {
		t.Fatalf("parallel phase: %+v", p)
	}
	activity("two", "skill_index", false, loop.Failed)
	p = snapshot()
	if p.ToolName == nil || *p.ToolName != "Reading skill" {
		t.Fatalf("remaining active tool lost: %+v", p)
	}
	if err := runtime.setPhase(t.Context(), protocol.TurnPhaseStopping); err != nil {
		t.Fatal(err)
	}
	activity("one", "read_skill", false, loop.OK)
	if err := runtime.setPhase(t.Context(), protocol.TurnPhaseStreaming); err != nil {
		t.Fatal(err)
	}
	p = snapshot()
	if p.Phase == nil || *p.Phase != protocol.TurnPhaseStopping || p.ToolName != nil {
		t.Fatalf("late completion overwrote stop: %+v", p)
	}
	if err := runtime.setActive(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	p = snapshot()
	if p.Active == nil || *p.Active || p.Phase != nil {
		t.Fatalf("idle phase: %+v", p)
	}
	if err := runtime.setPhase(t.Context(), protocol.TurnPhaseStopping); err != nil {
		t.Fatal(err)
	}
	if err := runtime.setActive(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	p = snapshot()
	if p.Phase == nil || *p.Phase != protocol.TurnPhaseWaitingModel {
		t.Fatalf("new turn kept old phase: %+v", p)
	}
}
