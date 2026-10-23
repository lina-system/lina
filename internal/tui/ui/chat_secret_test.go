// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
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
	for range len(secret.Placeholder) {
		m.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	if !m.conversation.composer.HasSelection() {
		t.Fatal("credential value was not selected")
	}
	m.Update(tea.PasteMsg{Content: "private-value"})
	if m.content.Draft.Text != "token="+secret.Placeholder {
		t.Fatal("selection replacement escaped masking")
	}
}

func TestConversationRedactedTokenContinuation(t *testing.T) {
	const suffix = "RemainingPrivateTokenSuffix"
	for _, tc := range []struct {
		name, prefix string
		typed        bool
	}{
		{"github", "ghp_" + strings.Repeat("a", 20), false},
		{"api-key", "sk-" + strings.Repeat("a", 16), false},
		{"bearer", "Bearer abcdefghijklmnop", false},
		{"assignment", "token=abcdefghijklmnop", false},
		{"typed", "ghp_" + strings.Repeat("a", 20), true},
		{"multiline", "한글\n확인 ghp_" + strings.Repeat("a", 20), false},
		{"near-limit", strings.Repeat("x", 16365) + " ghp_" + strings.Repeat("a", 20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: tc.prefix})
			masked := m.content.Draft.Text
			if !strings.HasSuffix(masked, secret.Placeholder) {
				t.Fatal("first fragment did not end in a redaction")
			}
			// More than one continuation must remain protected, even when an
			// entire insertion disappears and there is no draft change.
			for range 2 {
				if tc.typed {
					for _, r := range suffix {
						m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
					}
				} else {
					m.Update(tea.PasteMsg{Content: suffix})
				}
				if m.content.Draft.Text != masked || strings.Contains(m.View().Content, suffix) {
					t.Fatalf("credential continuation survived: %q", strings.TrimPrefix(m.content.Draft.Text, strings.Repeat("x", 16365)))
				}
			}
			m.Update(tea.PasteMsg{Content: " 끝"})
			if m.content.Draft.Text != masked+" 끝" {
				t.Fatal("ordinary text after a separator was lost")
			}
			m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
			m.Update(tea.PasteMsg{Content: suffix})
			if m.content.Draft.Text != masked {
				t.Fatal("undo lost the redacted insertion boundary")
			}
		})
	}
}

func TestConversationRedactedTokenSelectionAndReset(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "ghp_" + strings.Repeat("a", 20) + " public"})
	m.conversation.composer.SetCursorColumn(len(secret.Placeholder))
	for range len(" public") {
		m.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	}
	if !m.conversation.composer.HasSelection() {
		t.Fatal("text after the redaction was not selected")
	}
	m.Update(tea.PasteMsg{Content: "PrivateSuffix end"})
	if m.content.Draft.Text != secret.Placeholder+" end" {
		t.Fatalf("selection replacement lost the redacted boundary: %q", m.content.Draft.Text)
	}
	m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if m.content.Draft.Text != secret.Placeholder+" public" {
		t.Fatal("undo did not restore the safe draft")
	}
	m.conversation.composer.SelectAll()
	m.Update(tea.PasteMsg{Content: "ordinary"})
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.content.Draft.Text != "ordinaryx" {
		t.Fatal("replacing the placeholder kept a stale redaction boundary")
	}
}

func TestConversationRedactedTokenInsertionBoundaries(t *testing.T) {
	for _, separator := range []string{" ", "\n", ",", ";", `"`, "'", ".", ")"} {
		t.Run(separator, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: "앞 ghp_" + strings.Repeat("a", 20) + " 뒤"})
			m.conversation.composer.SetCursorColumn(len([]rune("앞 " + secret.Placeholder)))
			m.Update(tea.PasteMsg{Content: "PrivateSuffix" + separator + "public"})
			if want := "앞 " + secret.Placeholder + separator + "public 뒤"; m.content.Draft.Text != want {
				t.Fatalf("insertion boundary = %q, want %q", m.content.Draft.Text, want)
			}
		})
	}
}

func TestConversationRedactedTokenRecall(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:main:fixture", Items: []state.Block{
		{ID: "masked", Text: "한글 " + secret.Placeholder, Speaker: protocol.ConversationSpeakerPerson, Done: true},
	}}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.content.Draft.Text != "한글 "+secret.Placeholder {
		t.Fatal("masked history input was not recalled")
	}
	m.Update(tea.PasteMsg{Content: "PrivateSuffix end"})
	if m.content.Draft.Text != "한글 "+secret.Placeholder+" end" {
		t.Fatal("recalled input lost its redaction boundary")
	}
}
