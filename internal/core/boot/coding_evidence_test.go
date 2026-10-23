// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestCodingDeliveryEvidenceStaysWithinAcceptedScope(t *testing.T) {
	for _, mode := range []string{"current", "person", "agent", "conversation", "receipt", "generation", "moved-head", "failed-check"} {
		t.Run(mode, func(t *testing.T) {
			r, a, d := conformanceFixture(t)
			rr, err := r.beginConformance(t.Context(), a, &d)
			if err != nil {
				t.Fatal(err)
			}
			r.agent, r.conversation = a.Owner, a.Conversation
			r.coding = &codingTurn{Assignment: a, Phase: "delivery", Result: &d, Review: &rr}
			switch mode {
			case "person":
				r.person = "foreign"
			case "agent":
				r.agent = "foreign"
			case "conversation":
				r.conversation = "foreign"
			case "receipt":
				rr.Receipt = "old-receipt"
			case "generation":
				rr.Generation++
			case "moved-head":
				if err = r.work.ObserveHead(t.Context(), a.ID, "run", strings.Repeat("c", 40)); err != nil {
					t.Fatal(err)
				}
			case "failed-check":
				d.PR.Checks[0].Conclusion = "FAILURE"
			}
			raw, err := r.codingDeliveryEvidence(t.Context())
			if mode != "current" {
				if err == nil || len(raw) != 0 {
					t.Fatalf("out-of-scope evidence returned: %s %v", raw, err)
				}
				return
			}
			var observed struct {
				Source string
				Head   string
				PR     antigravity.PullRequest `json:"pull_request"`
			}
			if err != nil || json.Unmarshal(raw, &observed) != nil || observed.Source != "core_observation" || observed.Head != rr.Head || observed.PR.Head != rr.Head || len(observed.PR.Checks) == 0 {
				t.Fatalf("missing checked delivery: %s %v", raw, err)
			}
			r.personaMu = new(sync.Mutex)
			r.personaGrants = map[string]grant.Grant{}
			bindings := map[string]loop.Tool{}
			if _, err = r.prepareCodingEvidence("turn", bindings); err != nil {
				t.Fatal(err)
			}
			for _, args := range []string{`{"task_id":"foreign"}`, `{"path":"/other"}`, `{"command":"run"}`} {
				if _, err = bindings["read_coding_delivery"].Validate(provider.ToolCall{ID: "read", Name: "read_coding_delivery", Arguments: args}); err == nil {
					t.Fatalf("model expanded evidence scope: %s", args)
				}
			}
		})
	}
}
