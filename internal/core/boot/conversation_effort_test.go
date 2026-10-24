// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/loop"
)

// A configured Claude effort is the conversation model's effort. Preparation
// must hand it to the provider and record it with the request.
func TestConversationCarriesConfiguredEffort(t *testing.T) {
	r, p := turnRuntime(t)
	configured, err := modelpath.MessagesFromEnv(func(name string) string {
		return map[string]string{
			"LINA_CLAUDE_API_MODEL":  "configured-model",
			"LINA_CLAUDE_API_KEY":    "synthetic-key",
			"LINA_CLAUDE_API_EFFORT": "medium",
		}[name]
	})
	if err != nil || configured == nil || configured.Request(nil).Effort != "medium" {
		t.Fatalf("configured effort: %+v %v", configured, err)
	}
	r.route.Effort = configured.Effort
	p.steps = []turnStep{{events: turnText("Fixture reply.")}}
	input, inboxID := admitTurnInput(t, r, "effort-input")
	if err := r.run(t.Context(), &loop.Inputs{}, input, inboxID); err != nil {
		t.Fatal(err)
	}
	assertTurnRequests(t, p, 1)
	if p.requests[0].Effort != "medium" {
		t.Fatalf("provider effort = %q, want medium", p.requests[0].Effort)
	}
	recorded, err := r.journal().Get(t.Context(), "request:turn:effort-input:0")
	if err != nil {
		t.Fatal(err)
	}
	var evidence ledger.RequestEvidence
	if err := json.Unmarshal([]byte(recorded.Body.Text()), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Effort != "medium" || evidence.Model != "turn-fixture" {
		t.Fatalf("recorded request model=%s effort=%s", evidence.Model, evidence.Effort)
	}
}
