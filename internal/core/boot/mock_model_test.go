// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/results/mocks"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
)

func TestMockModelAcceptsReasoningBeforePayload(t *testing.T) {
	r, p := turnRuntime(t)
	want := `{"checklist":[{"id":"title","description":"Tasks","action":"none","target":""}]}`
	message, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": want}}})
	p.steps = []turnStep{{events: []provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"type":"reasoning","id":"reasoning-example","summary":[]}`)}, {Kind: provider.ItemDone, Item: message}, {Kind: provider.Terminal}}}}
	got, err := r.mockModel(t.Context(), "mock-run", mocks.ModelInput{Phase: "checklist", Request: "Tasks"})
	if err != nil || string(got) != want {
		t.Fatalf("valid reasoning response rejected: body=%s error=%v", got, err)
	}
	if len(p.streams) != 1 || !p.streams[0].closed {
		t.Fatal("model stream not closed")
	}
}
