// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSurfaceTextChoiceAndActionKeys(t *testing.T) {
	raw, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/surface.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	card := envelope.Payload.(protocol.Result)
	card.ResultID = "choice"
	card.Revision = "3"
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.SurfaceResultsCapability())
	m.conversation.id = "main:agent"

	block := state.Block{ID: "card", Kind: protocol.KindResult, Topic: "conversation:main:agent", Done: true}
	projectConversationBlock(&block, card)
	m.content.Blocks = []state.Block{block}
	m.refreshResultStreams(state.Snapshot{})
	m.openResultCard()
	if !strings.Contains(m.conversation.pager.text, "[a]") || !strings.Contains(m.conversation.pager.text, "[3]") {
		t.Fatal("missing card text")
	}
	if cmd := m.pagerKey(tea.KeyPressMsg{Code: '2', Text: "2"}, "2"); cmd != nil {
		t.Fatal("selection sent an action")
	}
	field := card.SurfaceControls.Fields[0]
	if !strings.Contains(strings.Join(m.pagerLines(), "\n"), field.Label+": "+field.Choices[1].Label) {
		t.Fatal("selected value not visible in rendered pager")
	}
	m.pagerKey(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, "ctrl+e")
	m.pagerKey(tea.KeyPressMsg{Code: 'x', Text: "EDIT-VISIBLE"}, "x")
	if !strings.Contains(strings.Join(m.pagerLines(), "\n"), "EDIT-VISIBLE") {
		t.Fatal("field draft not visible in rendered pager")
	}
	m.pagerKey(tea.KeyPressMsg{Code: tea.KeyEscape}, "esc")
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

func surfaceChoiceShell(count int) (*shellModel, *surfacePager) {
	m := conversationShell()
	field := protocol.SurfaceControlField{ID: "/pick", Kind: "choice", Label: "Pick", Value: json.RawMessage(`["1"]`)}
	for i := 1; i <= count; i++ {
		value := "v" + strconv.Itoa(i)
		field.Choices = append(field.Choices, protocol.SurfaceControlChoice{Key: strconv.Itoa(i), Label: value, Value: value})
	}
	p := &surfacePager{
		id:       "card",
		revision: "2",
		controls: protocol.SurfaceControls{
			Fields:  []protocol.SurfaceControlField{field},
			Actions: []protocol.SurfaceControlAction{{Key: "a", Name: "choose", Bindings: map[string]string{"pick": "/pick"}, Values: map[string]json.RawMessage{}}},
		},
		values:  map[string]json.RawMessage{},
		editing: -1,
	}
	m.conversation.pager = textPager{open: true, surface: p}
	return m, p
}

func TestSurfaceMultiDigitChoiceKeys(t *testing.T) {
	m, p := surfaceChoiceShell(12)
	if cmd := m.pagerKey(tea.KeyPressMsg{Code: '1', Text: "1"}, "1"); cmd != nil {
		t.Fatal("first digit of an ambiguous choice sent a command")
	}
	if p.values["/pick"] != nil {
		t.Fatalf("first digit selected a different option: %s", p.values["/pick"])
	}
	m.pagerKey(tea.KeyPressMsg{Code: '2', Text: "2"}, "2")
	if string(p.values["/pick"]) != `["v12"]` {
		t.Fatalf("multi-digit choice: %s", p.values["/pick"])
	}
}

func TestSurfacePendingChoiceCommitsBeforeAction(t *testing.T) {
	m, p := surfaceChoiceShell(12)
	m.pagerKey(tea.KeyPressMsg{Code: '1', Text: "1"}, "1")
	cmd := m.pagerKey(tea.KeyPressMsg{Code: 'a', Text: "a"}, "a")
	if cmd == nil {
		t.Fatal("action key ignored after a pending digit")
	}
	request := cmd().(shellManagement).Request.(protocol.SurfaceAction)
	if string(request.Values["pick"]) != `["v1"]` {
		t.Fatalf("pending choice not submitted: %s", request.Values["pick"])
	}
	if !p.busy {
		t.Fatal("submitted action did not mark the card busy")
	}
}

func TestSurfaceActionAcknowledgementBindsToSubmittingPager(t *testing.T) {
	m, p := surfaceChoiceShell(3)
	p.inputID, p.busy = "press-new", true
	reply := "reply"
	s := managementSession{model: m, pending: map[string]protocol.Payload{"reply": protocol.SurfaceAction{ResultID: "old", Revision: "1", Action: "choose", InputID: "press-old"}}}
	if handled, err := s.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Kind: protocol.KindItemDone}); !handled || err != nil {
		t.Fatalf("stale response: handled=%v err=%v", handled, err)
	}
	if !m.conversation.pager.open || m.conversation.pager.surface != p {
		t.Fatal("stale acknowledgement closed another card")
	}
	s.pending["reply"] = protocol.SurfaceAction{ResultID: "card", Revision: "2", Action: "choose", InputID: "press-new"}
	if handled, err := s.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Kind: protocol.KindItemDone}); !handled || err != nil {
		t.Fatalf("matching response: handled=%v err=%v", handled, err)
	}
	if m.conversation.pager.open {
		t.Fatal("matching acknowledgement left the card open")
	}
}

func TestSurfacePasteReachesActiveField(t *testing.T) {
	m, p := surfaceChoiceShell(3)
	p.editing = 0
	m.Update(tea.PasteMsg{Content: "pasted value"})
	if p.edit != "pasted value" {
		t.Fatalf("field draft=%q", p.edit)
	}
	if m.content.Draft.Text != "" {
		t.Fatalf("card paste reached the conversation draft: %q", m.content.Draft.Text)
	}
}
