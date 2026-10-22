// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
)

type activityTick time.Time

func formatElapsed(elapsed time.Duration) string {
	seconds := int64(max(0, elapsed) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%dh %02dm %02ds", seconds/3600, seconds/60%60, seconds%60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func (m *shellModel) activityVisible() bool {
	return (m.content.Busy || m.content.TurnActive) && m.status.State == Live && m.hasComposer() &&
		m.size.Width >= 40 && m.size.Height >= 10 && !m.accounts.open && !m.channels.open && !m.talk.open &&
		m.content.CardView.ID == "" && (!m.conversation.pager.open || m.size.Width >= 120)
}

func (m *shellModel) staticActivity() bool {
	return m.styles.options.ReducedMotion || m.styles.options.Profile == Monochrome
}

func (m *shellModel) activityInterval() time.Duration {
	if !m.activityVisible() {
		return 0
	}
	millis := m.styles.tokens.MotionActivityFrame
	if m.staticActivity() {
		millis = m.styles.tokens.MotionActivityElapsed
	}
	return time.Duration(millis) * time.Millisecond
}

func (m *shellModel) activityText() string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	word := "activity.thinking"
	if m.content.Activity.Replying {
		word = "activity.replying"
	}
	if m.content.Stopping {
		word = "activity.stopping"
	}
	elapsed := "0s" // Pending admission has no server start yet.
	if m.content.TurnActive {
		elapsed = text("unknown")
		if !m.content.Activity.Started.IsZero() {
			elapsed = formatElapsed(m.content.ServerNow.Sub(m.content.Activity.Started))
		}
	}
	frames, separator := m.styles.tokens.GlyphUnicodeActivityFrames, m.styles.tokens.GlyphUnicodeActivitySeparator
	if m.styles.options.ASCII {
		frames, separator = m.styles.tokens.GlyphAsciiActivityFrames, m.styles.tokens.GlyphAsciiActivitySeparator
	}
	glyphs := []rune(frames)
	frame := uint64(0)
	if !m.staticActivity() {
		frame = m.activityFrame % uint64(len(glyphs))
	}
	line := fmt.Sprintf("%c %s (%s %s %s)", glyphs[frame], text(word), elapsed, separator, text("activity.stop"))
	return render.Truncate(line, m.size.Width, render.Unicode{})
}

// The connection owner owns this timer. There is no goroutine or timer while
// idle; a stopped/hidden row also invalidates an already queued tick.
type activityClock struct {
	timer    *time.Timer
	C        <-chan time.Time
	interval time.Duration
}

func (c *activityClock) sync(interval time.Duration) {
	if interval == c.interval {
		return
	}
	c.stop()
	if interval > 0 {
		c.timer = time.NewTimer(interval)
		c.C, c.interval = c.timer.C, interval
	}
}
func (c *activityClock) next() {
	if c.timer != nil {
		c.timer.Reset(c.interval)
	}
}
func (c *activityClock) stop() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer, c.C, c.interval = nil, nil, 0
}
