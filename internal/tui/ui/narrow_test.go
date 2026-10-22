// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestNarrowActivityKeepsWholeEssentials(t *testing.T) {
	for _, lang := range []design.Language{design.English, design.Korean} {
		for _, width := range []int{79, 50, 49, 40} {
			for _, elapsed := range []time.Duration{0, 59 * time.Second, time.Hour, time.Duration(1<<63 - 1)} {
				t.Run(fmt.Sprintf("%s/%d/%s", lang, width, elapsed), func(t *testing.T) {
					m := conversationShell()
					m.styles = NewStyles(Options{Language: lang, Profile: Monochrome})
					m.Update(tea.WindowSizeMsg{Width: width, Height: 10})
					m.content.TurnActive = true
					m.content.Activity.Started = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					m.content.ServerNow = m.content.Activity.Started.Add(elapsed)
					line := m.activityText()
					hint := design.Text(lang, "activity.stop")
					if width < 50 {
						hint = "Esc"
					}
					for _, want := range []string{design.Text(lang, "activity.thinking"), formatElapsed(elapsed), hint + ")"} {
						if !strings.Contains(line, want) {
							t.Errorf("activity lost complete segment %q: %q", want, line)
						}
					}
					for _, row := range strings.Split(line, "\n") {
						if (render.Unicode{}).Width(row) > width {
							t.Errorf("activity row overflows %d cells: %q", width, row)
						}
					}
				})
			}
		}
	}
}

func TestNarrowFooterOmitsWholeHints(t *testing.T) {
	for _, lang := range []design.Language{design.English, design.Korean} {
		for width := 40; width <= 80; width++ {
			m := conversationShell()
			m.styles = NewStyles(Options{Language: lang, Profile: Monochrome})
			m.Update(tea.WindowSizeMsg{Width: width, Height: 10})
			rows := strings.Split(ansi.Strip(m.View().Content), "\n")
			footer := strings.TrimRight(rows[9], " ")
			parts := strings.Split(footer, "  ")
			for _, part := range parts[1:] {
				switch part {
				case "/help", "Enter", "Ctrl+D", "/memory", "/agents", "Ctrl+T":
				default:
					t.Errorf("%s/%d: footer cuts hint %q: %q", lang, width, part, footer)
				}
			}
		}
	}
}

func TestNarrowActivityWrapsDetailsAsOneRow(t *testing.T) {
	for _, width := range []int{79, 50, 40} {
		label := "◐ " + strings.Repeat("한", (width-6)/2)
		elapsed, separator := "1h 00m 00s", "·"
		hint := design.Text(design.English, "activity.stop")
		if width < 50 {
			hint = "Esc"
		}
		want := label + "\n(" + elapsed + " " + separator + " " + hint + ")"
		if got := formatActivity(width, label, elapsed, separator, design.Text(design.English, "activity.stop")); got != want {
			t.Errorf("%d: wrap must preserve complete word and details: %q", width, got)
		}
	}
}

func TestNarrowWrappedActivityReservesRowsAndListCounts(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "running", Version: "1.0"}, protocol.Capability{Name: "waiting", Version: "1.0"})
	m.content.Blocks = []state.Block{{ID: "r", Topic: "running", Text: "running"}, {ID: "w", Topic: "waiting", Text: "waiting"}}
	m.content.Draft.Text = "a\nb\nc\nd\ne\nf"
	m.content.TurnActive = true
	m.styles = NewStyles(Options{Profile: Monochrome})
	// Widen a token to exercise the otherwise rare wrap with today's short
	// catalogs. This checks measured rows, not a hard-coded language length.
	m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	m.styles.tokens.GlyphUnicodeActivitySeparator = strings.Repeat("·", 30)
	p := m.conversationPlan()
	if p.Activity.Height != 2 || p.Activity.Y+2 != p.Composer.Y || p.Conversation.Height < 4 {
		t.Fatalf("wrapped activity lost row reservation/floor: %+v", p)
	}
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	want := strings.Split(m.activityText(), "\n")
	for i, line := range want {
		if strings.TrimRight(rows[p.Activity.Y+i], " ") != line {
			t.Fatalf("activity row %d missing from view", i)
		}
	}
	for _, topic := range []string{"running", "waiting"} {
		if !strings.Contains(strings.Join(rows, "\n"), design.Text(design.English, topic)+" 1") {
			t.Errorf("folding lost %s count", topic)
		}
	}
}
