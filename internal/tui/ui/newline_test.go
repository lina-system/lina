// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
)

func TestComposerNewlineKeys(t *testing.T) {
	for _, flags := range []int{0, 1, 3} {
		m := conversationShell()
		m.Update(tea.KeyboardEnhancementsMsg{Flags: flags})
		m.Update(tea.PasteMsg{Content: "first"})
		for _, key := range []tea.KeyPressMsg{
			{Code: tea.KeyEnter, Mod: tea.ModShift},
			{Code: tea.KeyEnter, Mod: tea.ModAlt},
			{Code: 'j', Mod: tea.ModCtrl},
		} {
			before := m.content.Draft.Text
			m.Update(tea.KeyReleaseMsg(key))
			if m.content.Draft.Text != before {
				t.Fatalf("flags=%d: release changed draft", flags)
			}
			m.Update(key)
			if m.content.Draft.Text != before+"\n" || len(m.content.Blocks) != 0 {
				t.Fatalf("flags=%d key=%s: draft=%q, blocks=%d; want newline without send", flags, key, m.content.Draft.Text, len(m.content.Blocks))
			}
			key.IsRepeat = true
			m.Update(key)
			if m.content.Draft.Text != before+"\n\n" {
				t.Fatalf("flags=%d key=%s: editing repeat lost", flags, key)
			}
		}
		m.Update(tea.PasteMsg{Content: "last"})
		want := m.content.Draft.Text
		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if cmd == nil || m.content.Draft.Text != "" || len(m.content.Blocks) != 1 {
			t.Fatalf("flags=%d: Enter did not send one draft", flags)
		}
		request := cmd().(shellEffect).Message.(state.SendRequest)
		if request.Draft.Text != want {
			t.Fatalf("submitted %q, want %q", request.Draft.Text, want)
		}
	}
}

func TestComposerNewlineHintFollowsKeyboardReply(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		m := conversationShell()
		m.styles = NewStyles(Options{Width: 100, Profile: TrueColor, Language: language})
		m.conversation = newConversationView(m.styles)
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
		for _, flags := range []int{0, 1, 3, 0} {
			m.Update(tea.KeyboardEnhancementsMsg{Flags: flags})
			m.View()
			want, absent := "Alt+Enter", "Shift+Enter"
			if flags != 0 {
				want, absent = absent, want
			}
			if hint := m.conversation.composer.Placeholder; !strings.Contains(hint, want) || strings.Contains(hint, absent) {
				t.Fatalf("language=%s flags=%d: placeholder=%q, want %s", language, flags, hint, want)
			}
			// A reply updates presentation without consuming or moving a draft.
			m.Update(tea.PasteMsg{Content: "한글 draft"})
			before := m.View().Cursor
			m.Update(tea.KeyboardEnhancementsMsg{Flags: flags})
			after := m.View().Cursor
			if m.content.Draft.Text != "한글 draft" || *before != *after {
				t.Fatal("keyboard reply changed draft/cursor")
			}
			m.reduceInput(state.EditDraft{})
		}
	}
}
