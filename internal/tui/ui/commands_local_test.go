// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestUnknownCommandsStayLocal(t *testing.T) {
	for _, input := range []string{"/not-a-command hello", "/note\tFocus on section two", "/accounts unexpected", "/queue"} {
		t.Run(input, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: input})
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil || len(m.content.Blocks) != 0 || !m.conversation.pager.open {
				t.Fatalf("command escaped local guidance: cmd=%v blocks=%d pager=%v", cmd != nil, len(m.content.Blocks), m.conversation.pager.open)
			}
		})
	}
}

func TestUsageUnreportedForEveryAccount(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		m := accountShell()
		m.styles = NewStyles(Options{Width: 120, Profile: Monochrome, Language: language})
		m.accountRows = []protocol.AccountSummary{{AccountID: "hidden-a", Label: "First", Plan: stringPointer("Plus"), Status: "ok"}, {AccountID: "hidden-b", Label: "Second", Status: "usage_capped"}}
		text := m.accountContent()
		if strings.Count(text, design.Text(language, "progress.unknown")) < 2 || strings.Contains(text, "hidden-") || strings.Contains(text, "0%") {
			t.Fatalf("unreported quota is not unknown per account: %s", text)
		}
	}
}

func TestNoteWithoutTextGivesLocalGuidance(t *testing.T) {
	for _, input := range []string{"/note", "/note a-run"} {
		m := conversationShell()
		m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"})
		m.Update(tea.PasteMsg{Content: input})
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd != nil || !m.conversation.pager.open || len(m.content.Blocks) != 0 {
			t.Fatalf("incomplete note was sent: %s", input)
		}
	}
}

func TestNoteTargetsOneActiveBackgroundRun(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"})
	m.content.Blocks = []state.Block{{ID: "background", Topic: "running", RunID: "background", RunState: protocol.RunningStateInProgress}, {ID: "worker", Topic: "running", RunID: "session:worker", RunState: protocol.RunningStateInProgress}}
	m.Update(tea.PasteMsg{Content: "/note Focus on synthetic section two"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("single background note did not produce a control request")
	}
	p, ok := cmd().(shellManagement).Request.(protocol.FollowUp)
	if !ok || p.RunID != "background" || p.Text != "Focus on synthetic section two" || p.ConversationID == nil || *p.ConversationID != "main:fixture" {
		t.Fatalf("incorrect note target or scope: %+v", p)
	}
	if len(m.content.Blocks) != 2 || m.content.Draft.Text != "" {
		t.Fatal("note became a conversation echo or retained submitted text")
	}
}

func TestNoteAmbiguityAndMissingTargetsStayLocal(t *testing.T) {
	for _, count := range []int{0, 2} {
		m := conversationShell()
		m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"})
		for i := 0; i < count; i++ {
			id := string(rune('a' + i))
			m.content.Blocks = append(m.content.Blocks, state.Block{ID: id, Topic: "running", RunID: id, RunState: protocol.RunningStateInProgress})
		}
		handled, cmd := m.ownerCommand("/note Focus on section two")
		if !handled || cmd != nil || !m.conversation.pager.open {
			t.Fatalf("ambiguous note was sent: candidates=%d", count)
		}
	}
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"})
	m.content.Blocks = []state.Block{{ID: "only", Topic: "running", RunID: "only", RunState: protocol.RunningStateInProgress}}
	for _, input := range []string{"/note 0123456789abcdef0123456789abcdef text", "/note session:missing text", "/note task:missing text"} {
		handled, cmd := m.ownerCommand(input)
		if !handled || cmd != nil {
			t.Fatalf("missing target redirected: %s", input)
		}
	}
}
