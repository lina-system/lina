// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
)

func TestComposerChromeAndCursor(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for _, language := range []design.Language{design.English, design.Korean} {
			m := conversationShell()
			m.styles = NewStyles(Options{Width: 100, Profile: TrueColor, ASCII: ascii, Language: language})
			m.conversation = newConversationView(m.styles)
			m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
			prompt, rule := "› ", "─"
			if ascii {
				prompt, rule = "> ", "-"
			}
			view := m.View()
			rows := strings.Split(view.Content, "\n")
			if ansi.Strip(rows[31]) != strings.Repeat(rule, 100) || !strings.Contains(rows[31], m.styles.Line.Render(strings.Repeat(rule, 100))) {
				t.Errorf("rule is missing or uses the wrong style: %q", rows[31])
			}
			placeholder, ok := design.Lookup(language, "composer.placeholder")
			if !ok || placeholder == "" {
				t.Fatal("missing localized placeholder")
			}
			placeholder = fmt.Sprintf(placeholder, "LINA")
			if strings.TrimRight(ansi.Strip(rows[32]), " ") != prompt+placeholder || view.Cursor.X != 2 {
				t.Errorf("empty composer: %q, cursor=%+v", rows[32], view.Cursor)
			}
			_, firstSize := utf8.DecodeRuneInString(placeholder)
			if !strings.Contains(rows[32], m.styles.Accent.Render(prompt)) || !strings.Contains(rows[32], m.styles.Muted.Render(placeholder[firstSize:])) {
				t.Error("prompt/placeholder styles missing")
			}
			m.Update(tea.PasteMsg{Content: "한글 초안"})
			view = m.View()
			if want := (render.Unicode{}).Width(prompt + "한글 초안"); view.Cursor.X != want {
				t.Errorf("Korean cursor=%+v, want column %d", view.Cursor, want)
			}
		}
	}
}

func TestComposerContinuationAndGrowth(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	// A glyph belongs to the first visible composer row even when the textarea
	// scrolls. Soft wraps and explicit newlines both keep a two-cell gutter.
	for _, draft := range []string{strings.Repeat("한", 20), "one\ntwo", "1\n2\n3\n4\n5\n6\n한글"} {
		m.reduceInput(state.EditDraft{})
		m.Update(tea.PasteMsg{Content: draft})
		view := m.View()
		plan := m.conversationPlan()
		rows := strings.Split(ansi.Strip(view.Content), "\n")
		if !strings.HasPrefix(rows[plan.Composer.Y], "› ") {
			t.Errorf("first visible row has no prompt: %q", rows[plan.Composer.Y])
		}
		for y := plan.Composer.Y + 1; y < plan.Composer.Y+plan.Composer.Height; y++ {
			if !strings.HasPrefix(rows[y], "  ") {
				t.Errorf("continuation not aligned: %q", rows[y])
			}
		}
		if strings.Contains(draft, "6") && plan.Composer.Height != 6 {
			t.Errorf("composer growth = %d, want 6", plan.Composer.Height)
		}
		if want := (render.Unicode{}).Width(strings.TrimRight(rows[view.Cursor.Y], " ")); view.Cursor.X != want {
			t.Errorf("cursor column = %d, want end of visible draft at %d", view.Cursor.X, want)
		}
		if view.Cursor.Y < plan.Composer.Y || view.Cursor.Y >= plan.Status.Y || view.Cursor.X < 2 || view.Cursor.X >= 40 {
			t.Errorf("cursor outside draft: %+v", view.Cursor)
		}
	}
}
