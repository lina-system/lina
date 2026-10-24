// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
)

func TestM1RecallPersonAuxiliaryInstalledCore(t *testing.T) {
	const fact = "My telescope is violet."
	var mode, calls, receipts atomic.Int32
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input, Tools        []json.RawMessage
			Model, Instructions string
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid provider request")
			return
		}
		if len(req.Tools) == 0 {
			calls.Add(1)
			if len(req.Input) != 2 || req.Model != "fixture-model" || req.Instructions != "" {
				t.Error("auxiliary route not isolated")
			}
			var message struct{ Content string }
			if len(req.Input) != 2 || json.Unmarshal(req.Input[1], &message) != nil {
				t.Error("invalid auxiliary input")
				emitResponse(w, `{"sentences":[]}`, true)
				return
			}
			var input struct {
				Question string
				Sources  []struct {
					ID       string
					Revision int64
					Text     string
				}
			}
			if json.Unmarshal([]byte(message.Content), &input) != nil {
				t.Error("invalid source pack")
				return
			}
			citation := coreexec.Citation{}
			for _, s := range input.Sources {
				if s.Text == fact {
					citation = coreexec.Citation{ID: s.ID, Revision: s.Revision}
				}
			}
			if citation.ID == "" {
				t.Error("auxiliary omitted prior person statement")
			}
			if mode.Load() == 2 {
				<-r.Context().Done()
				close(cancelled)
				return
			}
			raw, _ := json.Marshal(map[string]any{"sentences": []coreexec.Sentence{{Text: "Violet.", Citations: []coreexec.Citation{citation}}, {Text: "Uncited."}, {Text: "Unknown source.", Citations: []coreexec.Citation{{ID: "foreign"}}}}})
			emitResponse(w, string(raw), true)
			return
		}
		if mode.Load() == 0 {
			emitResponse(w, "Understood.", true)
			return
		}
		var last struct {
			Type, Output string
			Call         string `json:"call_id"`
		}
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Type == "function_call_output" && strings.HasPrefix(last.Call, "person-read") {
			receipts.Add(1)
			var result struct {
				Code   string
				Output struct {
					coreexec.PersonAnswer
					Error string `json:"error"`
				}
			}
			if json.Unmarshal([]byte(last.Output), &result) != nil {
				t.Error("invalid tool receipt")
			}
			if len(result.Output.Sources) == 0 {
				t.Error("tool receipt lost sources")
			}
			if mode.Load() == 1 {
				if result.Code != "ok" || len(result.Output.Answer) != 1 || len(result.Output.Answer[0].Citations) != 1 {
					t.Errorf("invalid cited receipt: %s", last.Output)
				}
			} else if result.Code != "failed" || result.Output.Error != "aux_timeout" || len(result.Output.Answer) != 0 {
				t.Errorf("invalid timeout receipt: %s", last.Output)
			}
			emitResponse(w, "The telescope is violet.", true)
			return
		}
		declared := false
		for _, raw := range req.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			declared = declared || d.Name == "recall_person"
		}
		if !declared {
			t.Error("live recall_person declaration missing")
		}
		id := "person-read-success"
		if mode.Load() == 2 {
			id = "person-read-timeout"
		}
		emitPersonaCall(w, id, "recall_person", map[string]any{"question": "What color is my telescope?", "query": "telescope", "depth": 1})
	}))
	defer server.Close()
	h, start := installedConversationHarnessWithin(t, 2*time.Minute, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "person-fact", fact)
	mode.Store(1)
	admittedTurn(t, h, c, conversation, "person-recall", "Recall my telescope color.")
	mode.Store(2)
	admittedTurn(t, h, c, conversation, "person-timeout", "Recall my telescope color again.")
	select {
	case <-cancelled:
	case <-h.ctx.Done():
		t.Fatal("auxiliary HTTP request survived cancellation")
	}
	if calls.Load() != 2 || receipts.Load() != 2 {
		t.Fatalf("auxiliary calls=%d receipts=%d", calls.Load(), receipts.Load())
	}
	c.Close()
	h.stopCore(core)
}
