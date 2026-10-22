// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestFirstRunIsPresentationOnly(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, profile := range []Profile{Monochrome, TrueColor} {
			m := conversationShell()
			m.styles = NewStyles(Options{Language: language, Profile: profile, ASCII: profile == Monochrome})
			m.content.Agents = []protocol.Agent{{AgentID: m.activeAgent(), Name: "Mira"}}
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:" + m.conversation.id, Items: []state.Block{{ID: "turn", Kind: protocol.KindTurnState}}}})
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "core", Items: []state.Block{{ID: "core", Text: "status"}}}})
			m.Update(tea.PasteMsg{Content: "draft"})
			view := m.View().Content
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "lina.") || !strings.Contains(plain, "Mira") || !strings.Contains(plain, design.Text(language, "live")) {
				t.Fatalf("missing first-run identity: %s", plain)
			}
			for _, id := range []string{"conversation.start.memory", "conversation.start.voice", "conversation.start.task"} {
				text, found := design.Lookup(language, id)
				if !found || strings.Count(plain, text) != 1 || !strings.Contains(view, m.styles.Muted.Render(text)) {
					t.Fatalf("missing muted localized suggestion %s", id)
				}
			}
			if len(m.content.Blocks) != 2 || m.content.Draft.Text != "draft" || (profile == Monochrome && strings.Contains(view, "\x1b")) {
				t.Fatal("first-run render changed state or monochrome emitted escapes")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if strings.Contains(ansi.Strip(m.View().Content), "lina.") {
				t.Fatal("local input echo did not hide first-run screen")
			}
		}
	}
}

func TestFirstRunFitsSmallTerminalAndDoesNotHideHistory(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:" + m.conversation.id}})
	view := m.View().Content
	if !strings.Contains(view, "lina.") {
		t.Fatal("usable narrow terminal lost first-run screen")
	}
	for _, row := range strings.Split(view, "\n") {
		if ansi.StringWidth(row) > 40 {
			t.Fatal("first-run screen overflows terminal")
		}
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:" + m.conversation.id, Cursor: 1, Items: []state.Block{{ID: "old", Text: "restored message", Kind: protocol.KindItemDone, Done: true}}}})
	view = m.View().Content
	if strings.Contains(view, "lina.") || !strings.Contains(view, "restored message") {
		t.Fatal("first-run screen obscured restored history")
	}
}

func TestFirstRunWaitsForHistoryAndPreservesSignIn(t *testing.T) {
	m := conversationShell()
	if strings.Contains(m.View().Content, "lina.") {
		t.Fatal("first-run screen appeared before history was known")
	}
	m.content.Capabilities = []protocol.Capability{{Name: "accounts", Version: "1.0"}}
	m.accountsKnown = true
	view := m.View().Content
	if !strings.Contains(view, "lina.") || !strings.Contains(view, design.Text(design.English, "account.empty")) {
		t.Fatal("first-run screen displaced the sign-in action")
	}
	m.conversation.agentMissing = true
	if strings.Contains(m.View().Content, "lina.") {
		t.Fatal("unavailable agent showed starter suggestions")
	}
}
