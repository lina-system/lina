// SPDX-License-Identifier: Apache-2.0

package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
	"time"
)

func TestFormatElapsed(t *testing.T) {
	for _, tt := range []struct {
		seconds int
		want    string
	}{{-1, "0s"}, {0, "0s"}, {59, "59s"}, {60, "1m 00s"}, {3599, "59m 59s"}, {3600, "1h 00m 00s"}, {3661, "1h 01m 01s"}} {
		if got := formatElapsed(time.Duration(tt.seconds) * time.Second); got != tt.want {
			t.Errorf("%ds: got %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestActivityFramesClockAndVisibility(t *testing.T) {
	for _, options := range []Options{
		{Profile: TrueColor}, {Profile: TrueColor, ASCII: true}, {Profile: TrueColor, ReducedMotion: true}, {Profile: Monochrome},
	} {
		m := conversationShell()
		m.styles = NewStyles(options)
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		m.Update(shellData{Generation: 1, Message: state.Turn{Active: true, Started: at}})
		m.Update(activityTick(at))
		before := m.activityText()
		m.Update(activityTick(at.Add(100 * time.Millisecond)))
		changed := m.activityText() != before
		if changed == m.staticActivity() {
			t.Fatalf("static=%v frame changed=%v", m.staticActivity(), changed)
		}
		m.Update(activityTick(at.Add(2 * time.Second)))
		if !strings.Contains(m.activityText(), "2s") {
			t.Fatal("elapsed did not advance")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if !strings.Contains(m.activityText(), design.Text(design.English, "activity.stopping")) {
			t.Fatal("Esc did not show stopping")
		}
		var clock activityClock
		clock.sync(m.activityInterval())
		if clock.C == nil {
			t.Fatal("active row has no timer")
		}
		m.openPager("overlay")
		clock.sync(m.activityInterval())
		if clock.C != nil || clock.timer != nil {
			t.Fatal("hidden row kept timer")
		}
		m.conversation.pager = textPager{}
		clock.sync(m.activityInterval())
		if clock.C == nil {
			t.Fatal("returning to active row did not restart timer")
		}
		m.Update(shellData{Generation: 1, Message: state.Turn{Active: false, At: at.Add(3 * time.Second)}})
		clock.sync(m.activityInterval())
		if clock.C != nil || clock.timer != nil {
			t.Fatal("idle retained timer")
		}
		before = m.View().Content
		for i := 0; i < 20; i++ {
			m.Update(activityTick(at.Add(time.Duration(i) * time.Second)))
		}
		if m.View().Content != before {
			t.Fatal("stale tick changed idle view")
		}
		clock.stop()
	}
	for _, key := range []string{"NO_COLOR", "LINA_REDUCED_MOTION"} {
		opts := environmentOptions(func(name string) string {
			if name == key {
				return "1"
			}
			return ""
		})
		m := newShell(opts, nil)
		if !m.staticActivity() {
			t.Fatalf("%s did not disable motion", key)
		}
	}
}

func TestActivityIdleRecordingWritesZeroBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m := conversationShell()
	m.styles = NewStyles(Options{Profile: TrueColor, Width: 100})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	output, err := term.NewRecorder(100, 34)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 100, 34, m.styles.options.Profile)
	defer renderer.close()
	show := func() {
		t.Helper()
		if err := renderer.show(m.View(), tea.WindowSizeMsg{Width: 100, Height: 34}); err != nil {
			t.Fatal(err)
		}
	}
	m.Update(tea.PasteMsg{Content: "idle"})
	show()
	settled, err := output.Wait(ctx, func(s term.Snapshot) bool {
		return s.AltScreen && !s.CursorHidden && s.CursorX == 4+m.styles.composerPromptWidth() && s.CursorY == 32
	})
	if err != nil {
		t.Fatal(err)
	}
	// Inject queued ticks deterministically, then observe several real frame
	// periods. The cursor above is the settlement barrier, never a sleep.
	for i := 0; i < 20; i++ {
		m.Update(activityTick(time.Now().Add(time.Duration(i) * 100 * time.Millisecond)))
		show()
	}
	idle, cancelIdle := context.WithTimeout(ctx, 350*time.Millisecond)
	defer cancelIdle()
	last, err := output.Wait(idle, func(s term.Snapshot) bool { return s.Bytes != settled.Bytes })
	if !errors.Is(err, context.DeadlineExceeded) || last.Bytes != settled.Bytes {
		t.Fatalf("idle wrote %d bytes: %v", last.Bytes-settled.Bytes, err)
	}
	t.Logf("idle output across 20 injected ticks and 350ms: %d bytes", last.Bytes-settled.Bytes)
}

func TestActivityCatalogWidthAndMutedCompletion(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		m := conversationShell()
		m.styles = NewStyles(Options{Profile: TrueColor, Width: 40, Language: language})
		m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
		m.content.TurnActive = true
		m.content.Activity.Started = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		m.content.ServerNow = m.content.Activity.Started.Add(3600 * time.Second)
		if got := (render.Unicode{}).Width(m.activityText()); got > 40 {
			t.Fatalf("activity width=%d", got)
		}
		if !strings.Contains(m.activityText(), design.Text(language, "activity.thinking")) {
			t.Fatal("activity omitted localized word")
		}
		m.content.TurnActive = false
		m.content.Blocks = []state.Block{{ID: "reply", Text: "reply", Done: true, Revision: 1, Speaker: protocol.ConversationSpeakerAgent}}
		m.content.Worked = map[string]time.Duration{"reply": 12 * time.Second}
		m.refreshBlocks()
		line := fmt.Sprintf(design.Text(language, "activity.worked_for"), "12s")
		if !strings.Contains(m.View().Content, m.styles.Muted.Render(line)) {
			t.Fatal("completion duration did not use muted style and catalog")
		}
	}
}

func TestActivityStopsWhenWidePagerFillsShortTerminal(t *testing.T) {
	m := conversationShell()
	m.content.TurnActive = true
	m.content.Draft.Text = "one\ntwo\nthree\nfour\nfive\nsix"
	m.openPager("details")
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	if !m.activityVisible() {
		t.Fatal("side pane hid the activity row")
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 10})
	if m.conversationPlan().SidePane.Height != 0 {
		t.Fatal("expected full-screen pager after shedding side pane")
	}
	if m.activityInterval() != 0 {
		t.Fatal("full-screen pager retained the activity timer")
	}
}
