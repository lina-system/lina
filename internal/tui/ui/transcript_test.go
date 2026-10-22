// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestTranscriptUsesCurrentVisibleBlocks(t *testing.T) {
	for _, width := range []int{80, 140} {
		m := conversationShell()
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m.Update(tea.PasteMsg{Content: "DRAFT-458"})
		topic := "conversation:" + m.conversation.id
		m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Items: []state.Block{
			{ID: "person", Kind: protocol.KindItemDone, Speaker: protocol.ConversationSpeakerPerson, Text: "PERSON-458", Done: true},
			{ID: "reply", Kind: protocol.KindResult, Speaker: protocol.ConversationSpeakerAgent, InlineReply: true, Text: "COMPLETE-458\nTAIL-458", Body: "COMPLETE-458\nTAIL-458", Done: true},
			{ID: "turn", Kind: protocol.KindTurnState, Text: "INTERNAL-458"},
		}}})
		m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl, IsRepeat: true})
		if m.conversation.pager.open {
			t.Fatal("repeat opened transcript")
		}
		m.Update(tea.KeyReleaseMsg{Code: 't', Mod: tea.ModCtrl})
		if m.conversation.pager.open {
			t.Fatal("release opened transcript")
		}
		_, cmd := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
		if cmd != nil || !m.conversation.pager.open {
			t.Fatal("Ctrl+T must open the transcript without an external effect")
		}
		text := strings.Join(m.pagerLines(), "\n")
		want := design.Text(m.styles.options.Language, "person") + "\nPERSON-458\n\nLINA\nCOMPLETE-458\nTAIL-458"
		if text != want {
			t.Fatalf("transcript changed message content/order: %q", text)
		}
		if !strings.Contains(m.View().Content, "TAIL-458") {
			t.Fatal("transcript did not render in the full-screen or side-pane pager")
		}
		m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: topic, Cursor: 1, Block: state.Block{ID: "reply", Revision: 2, Kind: protocol.KindItemDone, Speaker: protocol.ConversationSpeakerAgent, Text: "REPLACED-458"}}})
		text = strings.Join(m.pagerLines(), "\n")
		if strings.Contains(text, "COMPLETE-458") || !strings.Contains(text, "REPLACED-458") {
			t.Fatal("open transcript retained superseded text")
		}
		m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "2", Topic: topic}})
		if text := strings.Join(m.pagerLines(), "\n"); strings.Contains(text, "458") {
			t.Fatal("open transcript retained revoked text")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if m.content.Draft.Text != "DRAFT-458" || m.content.Stopping || m.conversation.pager.open {
			t.Fatal("closing transcript changed draft/turn")
		}
	}
}

func TestTranscriptSearchNextAndClosePreserveActiveDraft(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "DRAFT-458"})
	m.content.TurnActive = true
	m.content.Blocks = []state.Block{{ID: "reply", Speaker: protocol.ConversationSpeakerAgent, Text: "MATCH first\nmiddle\nMATCH last"}}
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m.Update(tea.KeyPressMsg{Code: 'M', Text: "MATCH"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	first := m.conversation.pager.offset
	if !m.conversation.pager.open || m.pagerLines()[first] != "MATCH first" {
		t.Fatal("search did not reach first match")
	}
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.conversation.pager.offset <= first || m.pagerLines()[m.conversation.pager.offset] != "MATCH last" {
		t.Fatal("n did not reach next match")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.content.Draft.Text != "DRAFT-458" || !m.content.TurnActive || m.content.Stopping {
		t.Fatal("search/close edited the draft or interrupted the active turn")
	}
}

func TestTranscriptSanitizesAndLabelsLocalAndStreamingText(t *testing.T) {
	m := conversationShell()
	m.content.Agents = []protocol.Agent{{AgentID: "fixture", Name: "AGENT-458\x1b]52;c;hidden\a"}}
	m.content.Blocks = []state.Block{
		{ID: "local", Local: true, Text: "LOCAL-458\x1b]52;c;hidden\a"},
		{ID: "stream", Topic: "conversation:main:fixture", Kind: protocol.KindOutputDelta, Text: "STREAM-458\u202e"},
		{ID: "core", Topic: "core", Text: "CORE-458"},
		{ID: "running", Topic: "running", Text: "RUNNING-458"},
	}
	m.refreshBlocks()
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !m.conversation.pager.open {
		t.Fatal("Ctrl+T did not open transcript")
	}
	text := strings.Join(m.pagerLines(), "\n")
	want := design.Text(m.styles.options.Language, "person") + "\nLOCAL-458\n\nAGENT-458\nSTREAM-458"
	if text != want {
		t.Fatalf("unsafe or unrelated content: %q", text)
	}
}
