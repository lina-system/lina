// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
)

func TestSteeringHandoffPreservesOnlyUndrainedInputs(t *testing.T) {
	first := provider.Item(`{"role":"user","content":"first"}`)
	second := provider.Item(`{"role":"user","content":"late"}`)
	runtime := &conversationRuntime{steering: map[string][]acceptedSteering{
		string(first):  {{event: ledger.Event{ID: "first"}, inboxID: 1}},
		string(second): {{event: ledger.Event{ID: "late"}, inboxID: 2}},
	}}
	inputs := &loop.Inputs{}
	queue := &conversationInputs{runtime: runtime, inputs: inputs}
	inputs.Push(first)
	drained := queue.Drain()
	if len(drained) != 1 {
		t.Fatal("steering not applied at boundary")
	}
	inputs.Push(second)
	remaining := queue.Close()
	if len(remaining) != 1 || string(remaining[0]) != string(second) {
		t.Fatal("late input lost")
	}
	if _, exists := runtime.takeSteering(first); exists {
		t.Fatal("applied steering would be dispatched twice")
	}
	pending, exists := runtime.takeSteering(remaining[0])
	if !exists || pending.event.ID != "late" || pending.inboxID != 2 {
		t.Fatal("follow-up lost durable identity")
	}
	if inputs.Push(first) {
		t.Fatal("closed turn accepted steering")
	}
}
