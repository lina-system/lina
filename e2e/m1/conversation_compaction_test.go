// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1ConversationCompactsRetainedHistory(t *testing.T) {
	t.Parallel()
	const turnCount = 40
	var turns, summaries atomic.Int32
	var relationshipCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Instructions string
			Input, Tools []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(req.Input)
		var last struct{ Role, Content string }
		if len(req.Input) > 0 {
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Role == "developer" && strings.Contains(last.Content, "unanswered_requests") && strings.Contains(last.Content, "omitted_detail") {
			n := summaries.Add(1)
			if req.Instructions == "" || len(req.Tools) == 0 || !strings.Contains(string(raw), "receipt 42") {
				t.Error("summary lost its exact earlier segment or gained tools/current input")
			}
			// Deliberately forget earlier receipts in every model reply. Core
			// must carry them through successive summaries itself.
			emitResponse(w, fmt.Sprintf(`{"unanswered_requests":"none","topics_and_conclusions":"Archives","completed_actions":"Archive recorded.","specific_values":"receipt %d","omitted_detail":"Repeated archive material"}`, 41+n), true)
			return
		}
		if relationshipCalls.Load() < 2 {
			if relationshipCalls.Add(1) == 1 {
				emitPersonaCall(w, "relationship", "record_relationship", map[string]any{"quote": "We are study partners."})
			} else {
				var result struct{ Output string }
				if json.Unmarshal(req.Input[len(req.Input)-1], &result) != nil || !strings.Contains(result.Output, `"code":"ok"`) {
					t.Error("relationship tool failed")
				}
				emitResponse(w, "Acknowledged.", true)
			}
			return
		}
		n := turns.Add(1)
		relationship := false
		for _, rawItem := range req.Input {
			var item struct{ Role, Content string }
			if json.Unmarshal(rawItem, &item) == nil && strings.Contains(item.Content, "relationship_view") && strings.Contains(item.Content, "We are study partners.") {
				relationship = true
				if item.Role != "user" {
					t.Error("relationship acquired instruction authority")
				}
			}
		}
		if !relationship {
			t.Errorf("turn %d lost optional relationship context despite fitting published history", n)
		}
		if summaries.Load() > 0 {
			hasSummary := false
			for _, rawItem := range req.Input {
				var item struct{ Role, Content string }
				if json.Unmarshal(rawItem, &item) != nil || item.Role != "user" {
					continue
				}
				start := strings.IndexByte(item.Content, '{')
				if start < 0 {
					continue
				}
				var summary struct {
					Values string `json:"specific_values"`
				}
				if json.Unmarshal([]byte(item.Content[start:]), &summary) == nil && strings.Contains(summary.Values, "receipt 42") && strings.Contains(summary.Values, fmt.Sprintf("receipt %d", 41+summaries.Load())) {
					hasSummary = true
				}
			}
			if !hasSummary {
				t.Error("turn lost its published structured summary")
			}
			if strings.Contains(string(raw), "First archive filler") {
				t.Error("summarized raw history was sent again")
			}
		}
		if n == turnCount+1 && strings.Count(string(raw), "Continue after restart.") != 1 {
			t.Error("restart lost or duplicated current input")
		}
		emitResponse(w, "Archive recorded.", true)
	}))
	t.Cleanup(server.Close)
	h, start := installedConversationHarness(t, server.URL)
	// Forty retained turns and two Core boots exceed the shared 30-second
	// default; size this scenario to its work like other multi-boot cases.
	ctx, cancel := context.WithTimeout(t.Context(), 12*operationTimeout)
	t.Cleanup(cancel)
	h.ctx = ctx
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "relationship", "We are study partners.")
	for i := range turnCount {
		filler := "archive data "
		if i == 0 {
			filler = "First archive filler "
		}
		input := fmt.Sprintf("Archive %d: receipt %d. ", i, 42+i) + strings.Repeat(filler, 500)
		frames := admittedTurn(t, h, c, conversation, fmt.Sprintf("archive-%d", i), input)
		found := false
		for _, frame := range frames {
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil && *item.Text == "Archive recorded." {
				found = true
			}
		}
		if !found {
			t.Errorf("long conversation turn %d did not receive its reply", i+1)
		}
	}
	beforeRestart := summaries.Load()
	if beforeRestart < 2 {
		t.Error("long history did not exercise successive compactions")
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "resume-archive", "Continue after restart.")
	if turns.Load() != turnCount+1 || summaries.Load() != beforeRestart {
		t.Errorf("turns=%d summaries=%d, want all turns and no new summary after restart", turns.Load(), summaries.Load())
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		var nodes, sources, inputs, rawBytes int
		if err := tx.QueryRowContext(h.ctx, `SELECT (SELECT count(*) FROM sum_node WHERE conversation_id=? AND status='ready'),(SELECT count(*) FROM sum_cover c JOIN sum_node n ON n.id=c.node_id WHERE n.conversation_id=? AND c.reason=''),(SELECT count(*) FROM conv_event WHERE conversation_id=? AND speaker_kind='person'),(SELECT sum(length(body)) FROM conv_event WHERE conversation_id=? AND speaker_kind='person')`, conversation, conversation, conversation, conversation).Scan(&nodes, &sources, &inputs, &rawBytes); err != nil {
			return err
		}
		if nodes < 1 || sources < 2 || inputs != turnCount+2 {
			t.Errorf("summary proof nodes=%d sources=%d raw inputs=%d, want published summary, covered sources, all raw inputs", nodes, sources, inputs)
		}
		if rawBytes <= 128000 {
			t.Errorf("raw history bytes=%d, must exceed the hard request budget", rawBytes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.stopCore(core)
}
