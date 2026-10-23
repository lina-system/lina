// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestSurfaceTextChoiceAndActionKeys(t *testing.T) {
	raw, err := os.ReadFile("../../../kit/result/testdata/choice.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := result.Validate(result.SurfaceType, raw, result.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	text := result.PlainText(body, nil)
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.SurfaceResultsCapability())
	m.conversation.id = "main:agent"
	payload := json.RawMessage(raw)
	card := protocol.Result{ResultID: "choice", Revision: "3", Type: protocol.ResultTypeSurface, Text: &text, Body: &payload}
	block := state.Block{ID: "card", Kind: protocol.KindResult, Topic: "conversation:main:agent", Done: true}
	projectConversationBlock(&block, card)
	m.content.Blocks = []state.Block{block}
	m.openResultCard()
	if !strings.Contains(m.conversation.pager.text, "[a]") || !strings.Contains(m.conversation.pager.text, "[3]") {
		t.Fatal("missing card text")
	}
	if cmd := m.pagerKey(tea.KeyPressMsg{Code: '2', Text: "2"}, "2"); cmd != nil {
		t.Fatal("selection sent an action")
	}
	cmd := m.pagerKey(tea.KeyPressMsg{Code: 'a', Text: "a"}, "a")
	if cmd == nil {
		t.Fatal("action key ignored")
	}
	request := cmd().(shellManagement).Request.(protocol.SurfaceAction)
	if request.ResultID != "choice" || request.Revision != "3" || request.Action != "choose" || string(request.Values["slot"]) != `["wed-1500"]` || request.InputID == "" {
		t.Fatalf("action %+v", request)
	}
	if cmd := m.pagerKey(tea.KeyPressMsg{Code: 'a', Text: "a"}, "a"); cmd != nil {
		t.Fatal("pending action duplicated")
	}
	m.content.Blocks[0].ResultRevision = "4"
	m.refreshOpenCard()
	if m.conversation.pager.open {
		t.Fatal("stale action keys remained active")
	}
}
