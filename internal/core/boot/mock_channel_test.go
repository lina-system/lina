// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestChannelMockRetainsChecklistAndCanExport(t *testing.T) {
	r := runRuntime(t)
	r.conversation = "channel:mock"
	bot := channels.Bot{Platform: "telegram", Account: "fixture"}
	store := channels.New(r.db)
	if err := store.Connect(t.Context(), bot, r.person); err != nil {
		t.Fatal(err)
	}
	code, err := store.PairingCode(t.Context(), bot, r.person, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pair(t.Context(), bot, code, "42", "100", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = store.Bind(t.Context(), channels.Binding{Address: channels.Address{Bot: bot, Chat: "100"}, Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nsynthetic")
	token, err := r.assets.StageImport(t.Context(), bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.assets.Handover(t.Context(), token, "mock.png", assets.Lina, assets.Actor{PersonID: r.person, AgentID: r.agent})
	if err != nil {
		t.Fatal(err)
	}
	results, err := r.resultStore(ledger.Surface{Kind: "channel", Platform: "telegram", ChatID: "100"})
	if err != nil {
		t.Fatal(err)
	}
	body := result.Mock{Request: "Tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: a.ID, Revision: 1}, StopReason: "all_passed"}
	for _, id := range []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"} {
		body.Checks = append(body.Checks, result.MockCheck{ID: id, Passed: true})
	}
	raw, _ := json.Marshal(body)
	run := "channel-mock"
	card, err := results.PresentMock(t.Context(), result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: "turn", TaskID: &run}, result.PresentRequest{Type: result.MockType, Title: "Mock", Summary: "Checked", Body: raw}, run)
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT event_id FROM result_heads WHERE id=?", card.ID).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	event, err := r.journal().Get(t.Context(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	text, err := r.typedChannelText(t.Context(), event)
	if err != nil || !strings.Contains(text, "[1] Tasks") || !strings.Contains(text, "lina mock-export") {
		t.Fatalf("channel fallback: %q %v", text, err)
	}
	kind := "png"
	request := protocol.MockRequest{Action: "asset", ResultID: card.ID, Revision: "1", ConversationID: r.conversation, Asset: &kind}
	got, err := r.mockOperation(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{MessageID: "export", Payload: request})
	if err != nil {
		t.Fatal(err)
	}
	answer := got.Payload.(protocol.MockResponse)
	data, err := base64.StdEncoding.DecodeString(*answer.Data)
	if err != nil || !bytes.Equal(data, png) {
		t.Fatal("channel export lost bytes")
	}
	if _, err = r.mockOperation(t.Context(), clientsrv.Session{PersonID: "other"}, protocol.Envelope{Payload: request}); err == nil {
		t.Fatal("foreign channel export")
	}
}
