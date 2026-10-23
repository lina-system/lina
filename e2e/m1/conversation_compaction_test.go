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
	const turnCount = 16
	var turns, summaries atomic.Int32
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
			summaries.Add(1)
			if req.Instructions == "" || len(req.Tools) == 0 || !strings.Contains(string(raw), "receipt 42") {
				t.Error("summary lost its exact earlier segment or gained tools/current input")
			}
			emitResponse(w, `{"unanswered_requests":"none","topics_and_conclusions":"Archives","completed_actions":"First archive recorded.","specific_values":"receipt 42","omitted_detail":"Repeated archive material"}`, true)
			return
		}
		n := turns.Add(1)
		if summaries.Load() > 0 {
			if !strings.Contains(string(raw), "Reference summary") || !strings.Contains(string(raw), "receipt 42") {
				t.Error("turn lost its published summary")
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
	core := start(true)
	c, conversation := conversationClient(t, h)
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
	if beforeRestart == 0 {
		t.Error("long history was never compacted")
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
		var nodes, sources, inputs int
		if err := tx.QueryRowContext(h.ctx, `SELECT (SELECT count(*) FROM sum_node WHERE conversation_id=? AND status='ready'),(SELECT count(*) FROM sum_cover c JOIN sum_node n ON n.id=c.node_id WHERE n.conversation_id=? AND c.reason=''),(SELECT count(*) FROM conv_event WHERE conversation_id=? AND speaker_kind='person')`, conversation, conversation, conversation).Scan(&nodes, &sources, &inputs); err != nil {
			return err
		}
		if nodes < 1 || sources < 2 || inputs != turnCount+1 {
			t.Errorf("summary proof nodes=%d sources=%d raw inputs=%d, want published summary, covered sources, all raw inputs", nodes, sources, inputs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.stopCore(core)
}
