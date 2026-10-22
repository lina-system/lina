// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestShortcutSheetEmptyDraftAndHelp(t *testing.T) {
	for _, command := range []string{"?", "/help"} {
		t.Run(command, func(t *testing.T) {
			m := conversationShell()
			if command == "?" {
				m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
			} else {
				m.Update(tea.PasteMsg{Content: command})
				m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			if !m.conversation.pager.open || m.content.Draft.Text != "" {
				t.Fatalf("shortcut sheet not opened: open=%v draft=%q", m.conversation.pager.open, m.content.Draft.Text)
			}
			for _, key := range []string{"Esc", "Alt+Enter", "Shift+Enter", "PgUp/PgDn", "Ctrl+O", "Ctrl+T", "F6", "/channels", "/talk"} {
				if !strings.Contains(m.conversation.pager.text, key) {
					t.Errorf("shortcut sheet missing %s", key)
				}
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.conversation.pager.open || m.content.Stopping {
				t.Fatal("Esc did not close just the sheet")
			}
		})
	}
}

func TestShortcutQuestionRemainsDraftText(t *testing.T) {
	for _, draft := range []string{"why", " "} {
		m := conversationShell()
		m.Update(tea.PasteMsg{Content: draft})
		m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		if m.conversation.pager.open || m.content.Draft.Text != draft+"?" {
			t.Fatalf("question was intercepted in draft: %q", m.content.Draft.Text)
		}
	}
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "?"})
	if m.conversation.pager.open || m.content.Draft.Text != "?" {
		t.Fatal("pasted question must be text")
	}
}

func TestShortcutFooterKeepsWholePriorityPrefix(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, connection := range []Connection{Live, Retrying, Resyncing, Recovering} {
				m := conversationShell()
				m.styles = NewStyles(Options{Language: language, ASCII: ascii})
				for width := 40; width <= 240; width++ {
					m.size.Width = width
					m.status = ConnectionStatus{State: connection}
					status := m.styles.presentation(m.status)
					prefix := status.Mark + " " + status.Label
					for _, pager := range []bool{false, true} {
						m.conversation.pager.open = pager
						section := ""
						if pager {
							section = "pager"
						}
						bindings := bindingsFor(section)
						got := ansi.Strip(m.conversationFooter(false))
						if ansi.StringWidth(got) > width || !strings.HasPrefix(got, prefix+"  "+bindings[0].Keys[0]+" "+design.Text(language, bindings[0].Hint)) {
							t.Fatalf("connection/top hint lost at %d: %q", width, got)
						}
						remainder := strings.TrimPrefix(got, prefix)
						for _, binding := range bindings {
							if binding.Hint == "" {
								continue
							}
							hint := "  " + binding.Keys[0] + " " + design.Text(language, binding.Hint)
							if remainder == "" {
								break
							}
							if !strings.HasPrefix(remainder, hint) {
								t.Fatalf("partial or reordered hint at %d: %q", width, remainder)
							}
							remainder = strings.TrimPrefix(remainder, hint)
						}
						if remainder != "" {
							t.Fatalf("unrecognized footer suffix: %q", remainder)
						}
					}
					m.conversation.pager.open = false
					m.status.Cause = protocol.ErrorUnavailable
					got := ansi.Strip(m.conversationFooter(true))
					if ansi.StringWidth(got) > width || !strings.HasPrefix(got, prefix) || !strings.Contains(got, "? "+design.Text(language, "shortcuts.title")) {
						t.Fatalf("cause displaced connection/top hint at %d: %q", width, got)
					}
				}
			}
		}
	}
}

func TestShortcutLocalHelpAndInputGate(t *testing.T) {
	m := conversationShell()
	for _, msg := range []tea.KeyMsg{tea.KeyReleaseMsg{Code: '?', Text: "?"}, tea.KeyPressMsg{Code: '?', Text: "?", IsRepeat: true}} {
		m.Update(msg)
		if m.conversation.pager.open || m.content.Draft.Text != "" {
			t.Fatal("release/repeat opened sheet or edited draft")
		}
	}
	m.content.Draft.Attachments = []string{"fixture"}
	m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if m.conversation.pager.open || m.content.Draft.Text != "?" {
		t.Fatal("attachment draft was treated as empty")
	}
	m = conversationShell()
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Retrying}})
	m.Update(tea.PasteMsg{Content: "/help"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.conversation.pager.open {
		t.Fatal("local help unavailable during reconnect")
	}
}

func TestShortcutEnhancedQuestion(t *testing.T) {
	m := conversationShell()
	m.Update(tea.KeyPressMsg{Code: '/', ShiftedCode: '?', Text: "?", Mod: tea.ModShift})
	if !m.conversation.pager.open {
		t.Fatal("enhanced shifted question did not open sheet")
	}
}
