// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/secret"

	tea "charm.land/bubbletea/v2"
)

func TestConversationPasteSecretNeverReachesDisplay(t *testing.T) {
	m := conversationShell()
	token := "ghp_" + strings.Repeat("AlphaAcceptance", 3)
	m.Update(tea.PasteMsg{Content: "확인 " + token + " 끝"})
	if strings.Contains(m.View().Content, token) {
		t.Fatal("pasted credential was rendered in the composer before submission")
	}
	_, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("masked conversation could not be sent")
	}
	if strings.Contains(m.View().Content, token) {
		t.Fatal("pasted credential was rendered in the local conversation echo")
	}
}

func TestConversationPasteMasksBeforeLimitAndUndo(t *testing.T) {
	for _, tc := range []struct{ name, prefix, paste, forbidden string }{
		{"assignment", "token=", "private-value end", "private-value"},
		{"structured", "", `{"password":"private-value","count":9007199254740993}`, "private-value"},
		{"split-before-limit", strings.Repeat("x", 16360) + " ghp_" + strings.Repeat("a", 10), strings.Repeat("b", 30), "ghp_"},
		{"private-key", "", "-----BEGIN PRIVATE KEY-----\nprivate-value\n-----END PRIVATE KEY-----", "private-value"},
		{"deep-json", "", strings.Repeat("[", 66) + `{"password":"private-value"}` + strings.Repeat("]", 66), "private-value"},
		{"before-limit", strings.Repeat("x", 16376), " ghp_" + strings.Repeat("Abcd", 10), "ghp_"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: tc.prefix})
			m.Update(tea.PasteMsg{Content: tc.paste})
			if strings.Contains(m.content.Draft.Text, tc.forbidden) || strings.Contains(m.View().Content, tc.forbidden) {
				t.Fatal("pasted credential survived draft masking")
			}
			if tc.name == "structured" && !strings.Contains(m.content.Draft.Text, "9007199254740993") {
				t.Fatal("masking changed exact numeric evidence")
			}
			m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
			if m.content.Draft.Text != tc.prefix {
				t.Fatal("undo did not restore the pre-paste draft")
			}
		})
	}
}

func TestConversationPasteOrdinaryInsertionAndUndo(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "앞 뒤"})
	m.conversation.composer.SetCursorColumn(2)
	m.Update(tea.PasteMsg{Content: "가운데 "})
	if m.content.Draft.Text != "앞 가운데 뒤" || m.conversation.composer.Column() != 6 {
		t.Fatal("ordinary paste lost its insertion position")
	}
	m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if m.content.Draft.Text != "앞 뒤" {
		t.Fatal("ordinary paste was not one undo step")
	}
}

func TestConversationPasteMasksSelectionReplacement(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "token=public"})
	m.conversation.composer.SetCursorColumn(6)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModShift})
	m.Update(tea.PasteMsg{Content: "private-value"})
	if m.content.Draft.Text != "token="+secret.Placeholder {
		t.Fatal("selection replacement escaped masking")
	}
}
