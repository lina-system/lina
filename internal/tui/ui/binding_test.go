// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// chipShell draws the conversation at the acceptance size with the person's
// message first and one binding chip per case.
func chipShell() *shellModel {
	m := newShell(Options{Width: 120, Profile: Monochrome}, time.Now)
	m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	m.conversation.id = "main:fixture"
	m.size = layout.Size{Width: 120, Height: 34}
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	return m
}

func chipPerson(input, text, name, class string) state.Block {
	target := protocol.BindingTarget{Class: class}
	if class == "existing" {
		kind := "todo"
		target.ID, target.Kind = &input, &kind
	}
	return state.Block{ID: input, Topic: "conversation:fixture", Speaker: protocol.ConversationSpeakerPerson, Local: true, Text: text, Done: true, Revision: 1,
		Binding: &protocol.BindingChip{InputID: input, Show: true, Name: name, Target: target}}
}

func TestBindingChipShowsOneLineAtAcceptanceSize(t *testing.T) {
	m := chipShell()
	cases := []struct{ input, text, name, class, want string }{
		{"aged", "continue", "Garden", "existing", "Garden"},
		{"leaves", "plants again", "Plants", "existing", "Plants"},
		{"creates", "remember this", "새로 산 화분 물 주기", "new", "새로 산 화분 물 주기"},
		{"none", "just chatting", "", "none", "Not bound to an item"},
	}
	var items []state.Block
	for _, c := range cases {
		items = append(items, chipPerson(c.input, c.text, c.name, c.class))
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: uint64(len(items)), Items: items}})
	if len(m.content.Blocks) != len(cases) {
		t.Fatalf("blocks = %d", len(m.content.Blocks))
	}
	rows := conversationRows(m)
	joined := strings.Join(rows, "\n")
	for _, c := range cases {
		found := false
		for _, row := range rows {
			if strings.Contains(row, c.want) {
				found = true
				if len([]rune(strings.TrimRight(row, " "))) > 120 {
					t.Errorf("chip overflows one line: %q", row)
				}
				break
			}
		}
		if !found {
			t.Errorf("chip %q missing for %s\n%s", c.want, c.input, joined)
		}
	}
	if strings.Count(joined, "Bound to") != 3 {
		t.Errorf("expected three chips that name an item:\n%s", joined)
	}
}

func TestBindingChipHidesForFreshFocus(t *testing.T) {
	m := chipShell()
	block := chipPerson("focus", "carry on", "Garden", "existing")
	block.Binding.Show = false
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: 1, Items: []state.Block{block}}})
	if joined := strings.Join(conversationRows(m), "\n"); strings.Contains(joined, "Bound to") || strings.Contains(joined, "Not bound") {
		t.Fatalf("a fresh focus must show no chip:\n%s", joined)
	}
}

func TestBindingMoveListOpensOnCtrlB(t *testing.T) {
	m := chipShell()
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Cursor: 1, Items: []state.Block{chipPerson("one", "plants again", "Plants", "existing")}}})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if cmd == nil || !m.conversation.move.open || m.conversation.move.input != "one" {
		t.Fatalf("Ctrl+B did not open the move list: %+v", m.conversation.move)
	}
	effect := cmd()
	request, ok := effect.(shellEffect)
	if !ok {
		t.Fatalf("no request: %T", effect)
	}
	get, ok := request.Message.(state.BindingGetRequest)
	if !ok || get.InputID != "one" {
		t.Fatalf("wrong request: %+v", request.Message)
	}
	m.bindingResponse(protocol.Envelope{Payload: protocol.Binding{InputID: "one", Chip: protocol.BindingChip{InputID: "one", Show: true, Name: "Plants"}, Target: protocol.BindingTarget{Class: "existing"}, Candidates: []protocol.BindingCandidate{
		{ID: "garden", Title: "Garden", Kind: "project", State: "existing"},
		{ID: "new", Title: "", Kind: "todo", State: "new"},
		{ID: "none", Title: "", Kind: "", State: "none"},
	}}})
	joined := strings.Join(conversationRows(m), "\n")
	if !strings.Contains(joined, "Garden") || !strings.Contains(joined, "New item") || !strings.Contains(joined, "Not bound to an item") {
		t.Fatalf("move list missing choices:\n%s", joined)
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if cmd == nil {
		t.Fatal("choosing a candidate sent nothing")
	}
	move, ok := cmd().(shellEffect).Message.(state.BindingMoveRequest)
	if !ok || move.InputID != "one" || move.Target.Class != "existing" || move.Target.ID == nil || *move.Target.ID != "garden" {
		t.Fatalf("wrong move: %+v", move)
	}
	if !m.bindingResponse(protocol.Envelope{Payload: protocol.BindingMoveResult{MoveID: "move:one", InputID: "one"}}) {
		t.Fatal("move result was not consumed")
	}
	if m.conversation.move.open {
		t.Error("the list stayed open after the move")
	}
}
