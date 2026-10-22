// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func historyShell() *shellModel {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:main:fixture", Items: []state.Block{
		{ID: "first", Text: "first input", Speaker: protocol.ConversationSpeakerPerson, Done: true},
		{ID: "reply", Text: "agent reply", Speaker: protocol.ConversationSpeakerAgent, Done: true},
		{ID: "second", Text: "second input", Speaker: protocol.ConversationSpeakerPerson, Done: true},
	}}})
	return m
}

func historyKey(m *shellModel, key rune) { m.Update(tea.KeyPressMsg{Code: key}) }

func TestInputHistoryRecallAndRestoreDraft(t *testing.T) {
	m := historyShell()
	draft := state.Draft{Text: "unsent 한글", Attachments: []string{"attachment"}}
	m.reduceInput(state.EditDraft{Draft: draft})
	m.conversation.composer.SetCursorColumn(3)
	for _, step := range []struct {
		key  rune
		want string
	}{
		{tea.KeyUp, "second input"}, {tea.KeyUp, "first input"},
		{tea.KeyUp, "first input"}, {tea.KeyDown, "second input"},
		{tea.KeyDown, draft.Text}, {tea.KeyDown, draft.Text},
	} {
		historyKey(m, step.key)
		if m.content.Draft.Text != step.want {
			t.Fatalf("key %v: draft=%q, want %q", step.key, m.content.Draft.Text, step.want)
		}
	}
	if !reflect.DeepEqual(m.content.Draft, draft) || m.conversation.composer.Column() != 3 {
		t.Fatalf("draft or cursor not restored: %+v column=%d", m.content.Draft, m.conversation.composer.Column())
	}
}

func TestInputHistoryMultilineAndWrappedCursor(t *testing.T) {
	for _, draft := range []string{"one\ntwo\nthree", strings.Repeat("한글 ", 40)} {
		m := historyShell()
		m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
		m.Update(tea.PasteMsg{Content: draft})
		m.View()
		before := m.conversation.composer.Cursor().Y
		historyKey(m, tea.KeyUp)
		if m.content.Draft.Text != draft || m.conversation.composer.Cursor().Y >= before {
			t.Fatal("Up within multiline/wrapped input must only move the cursor")
		}
		for m.conversation.composer.Line() > 0 || m.conversation.composer.LineInfo().RowOffset > 0 {
			historyKey(m, tea.KeyUp)
		}
		historyKey(m, tea.KeyDown)
		if m.content.Draft.Text != draft {
			t.Fatal("Down inside multiline input changed the draft")
		}
		historyKey(m, tea.KeyUp)
		historyKey(m, tea.KeyUp)
		if m.content.Draft.Text != "second input" {
			t.Fatal("Up on first visual line did not recall history")
		}
		historyKey(m, tea.KeyDown)
		if m.content.Draft.Text != draft {
			t.Fatal("multiline draft was not restored")
		}
	}
}

func TestInputHistoryEditsStartANewDraft(t *testing.T) {
	m := historyShell()
	historyKey(m, tea.KeyUp)
	m.Update(tea.PasteMsg{Content: " corrected"})
	edited := m.content.Draft.Text
	historyKey(m, tea.KeyUp)
	historyKey(m, tea.KeyDown)
	if m.content.Draft.Text != edited || edited != "second input corrected" {
		t.Fatalf("edited recall lost: %q", m.content.Draft.Text)
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	historyKey(m, tea.KeyUp)
	historyKey(m, tea.KeyDown)
	if m.content.Draft.Text != "" {
		t.Fatal("clear resurrected the previous draft")
	}
}

func TestInputHistoryUsesCurrentAcceptedConversationOnly(t *testing.T) {
	m := historyShell()
	m.content.Blocks = append(m.content.Blocks,
		state.Block{ID: "other", Topic: "conversation:other", Text: "other person", Speaker: protocol.ConversationSpeakerPerson, Done: true},
		state.Block{ID: "local", Text: "unacknowledged", Local: true},
		state.Block{ID: "removed", Topic: "conversation:main:fixture", Text: "removed", Speaker: protocol.ConversationSpeakerPerson, Done: true, Removed: true},
	)
	m.Update(tea.PasteMsg{Content: "draft"})
	historyKey(m, tea.KeyUp)
	if m.content.Draft.Text != "second input" {
		t.Fatal("recall admitted output, another conversation or an unaccepted input")
	}
	// A fresh snapshot removes the selected entry. It must not survive in a
	// parallel text history or offset-based navigation cache.
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "2", Topic: "conversation:main:fixture", Items: []state.Block{
		{ID: "first", Text: "first input", Speaker: protocol.ConversationSpeakerPerson, Done: true},
	}}})
	m.View()
	if m.content.Draft.Text != "draft" {
		t.Fatal("removed recalled input remained in the composer")
	}
	historyKey(m, tea.KeyUp)
	if m.content.Draft.Text != "first input" {
		t.Fatal("fresh history not used")
	}
	m.reduceInput(state.FocusConversation{})
	historyKey(m, tea.KeyDown)
	if m.content.Draft.Text != "draft" {
		t.Fatal("focus change retained recalled text")
	}
}

func TestInputHistoryMaskedFieldsNeverJoinRecall(t *testing.T) {
	for _, kind := range []string{"token", "callback"} {
		t.Run(kind, func(t *testing.T) {
			m := historyShell()
			if kind == "token" {
				m.channels = channelView{open: true, stage: "token"}
			} else {
				m.accounts = accountView{open: true, stage: "sign_in_open"}
			}
			m.Update(tea.PasteMsg{Content: "synthetic-sensitive-value"})
			if strings.Contains(m.View().Content, "synthetic-sensitive") {
				t.Fatal("unmasked form")
			}
			historyKey(m, tea.KeyEnter)
			m.channels = channelView{}
			m.accounts = accountView{}
			for _, want := range []string{"second input", "first input", "first input"} {
				historyKey(m, tea.KeyUp)
				if m.content.Draft.Text != want {
					t.Fatalf("masked field contaminated recall: %q", m.content.Draft.Text)
				}
			}
		})
	}
}
