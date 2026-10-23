// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func projectWork(payload protocol.Payload) *state.WorkSummary {
	p, ok := payload.(protocol.ProgressUpdate)
	if !ok || p.Rows == nil {
		return nil
	}
	cards := []state.WorkCard{}
	clean := func(s string) string { return strings.Join(strings.Fields(sanitize.Display(s)), " ") }
	for _, row := range *p.Rows {
		v := row.Line
		if v.Kind == "goal" {
			continue
		}
		c := state.WorkCard{ID: row.Ref, Name: clean(v.Name)}
		if v.Now != nil {
			c.Now = clean(*v.Now)
		}
		if v.StartedAt != nil {
			c.Started, _ = time.Parse(time.RFC3339Nano, *v.StartedAt)
		}
		if v.FinishedAt != nil {
			c.Finished, _ = time.Parse(time.RFC3339Nano, *v.FinishedAt)
		}
		if v.State != nil {
			c.State = *v.State
		}
		c.Waiting, _ = strconv.Atoi(string(v.Waiting))
		cards = append(cards, c)
	}
	return &state.WorkSummary{Cards: cards}
}

func (m *shellModel) workCards() []state.WorkCard {
	if !m.hasCapability("progress") {
		return nil
	}
	for _, b := range m.content.Blocks {
		if b.Topic == "progress" && b.Work != nil {
			return b.Work.Cards
		}
	}
	return nil
}

func workElapsed(d time.Duration) string {
	minutes := int64(max(0, d) / time.Minute)
	if minutes >= 1440 {
		return fmt.Sprintf("%dd %02dh", minutes/1440, minutes/60%24)
	}
	if minutes >= 60 {
		return fmt.Sprintf("%dh %02dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}

func (m *shellModel) workPanelRows(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	ellipsis := "…"
	if m.styles.options.ASCII {
		ellipsis = "..."
	}
	rows := []string{text("work.title")}
	active := []state.WorkCard{}
	done := 0
	for _, c := range m.workCards() {
		if c.State == protocol.RunningStateDone && c.Waiting == 0 {
			done++
			continue
		}
		active = append(active, c)
	}
	available := height - 1
	if done > 0 {
		available--
	}
	shown := min(len(active), max(0, available/2))
	if shown < len(active) {
		shown = min(len(active), max(0, (available-1)/2))
	}
	now := m.now().Add(m.content.Offset)
	for _, c := range active[:shown] {
		elapsed := text("unknown")
		if !c.Started.IsZero() {
			end := now
			if !c.Finished.IsZero() {
				end = c.Finished
			}
			elapsed = workElapsed(end.Sub(c.Started))
		}
		name := panelClip(c.Name, max(0, width-(render.Unicode{}).Width(elapsed)-1), ellipsis)
		first := name + strings.Repeat(" ", max(1, width-(render.Unicode{}).Width(name)-(render.Unicode{}).Width(elapsed))) + elapsed
		activity := c.Now
		if activity == "" {
			activity = text("unknown")
		}
		rows = append(rows, first, "  "+panelClip(activity, max(0, width-2), ellipsis))
	}
	if done > 0 && len(rows) < height {
		rows = append(rows, fmt.Sprintf(text("work.done"), done))
	}
	if shown < len(active) && len(rows) < height {
		waiting := 0
		for _, c := range active[shown:] {
			if c.Waiting > 0 {
				waiting++
			}
		}
		more := fmt.Sprintf(text("work.more"), ellipsis, len(active)-shown)
		if waiting > 0 {
			more += " · " + fmt.Sprintf(text("work.needs"), waiting)
			if m.styles.options.ASCII {
				more = strings.ReplaceAll(more, " · ", " / ")
			}
		}
		rows = append(rows, panelClip(more, width, ellipsis))
	}
	return rows
}

func panelClip(s string, width int, tail string) string {
	u := render.Unicode{}
	if width <= 0 {
		return ""
	}
	if u.Width(tail) > width {
		return render.Truncate(s, width, u)
	}
	if u.Width(s) <= width {
		return s
	}
	// Grapheme slicing uses the same terminal width model as conversation text.
	return render.Truncate(s, max(0, width-u.Width(tail)), u) + tail
}

func (m *shellModel) workPanelKey(key string) tea.Cmd {
	switch key {
	case "ctrl+d":
		m.status = ConnectionStatus{State: Quit}
		return tea.Quit
	case "ctrl+c":
		return m.controlC()
	case "esc":
		m.conversation.workFocused = false
	case "up", "down":
		cards := m.workCards()
		index := 0
		for i, c := range cards {
			if c.ID == m.conversation.workSelected {
				index = i
			}
		}
		if len(cards) > 0 {
			if key == "up" {
				index = max(0, index-1)
			} else {
				index = min(len(cards)-1, index+1)
			}
			m.conversation.workSelected = cards[index].ID
		}
	}
	return nil
}

func (m *shellModel) workPanelInterval() time.Duration {
	if !m.hasCapability("progress") || m.size.Width < 40 || m.size.Height < 10 || m.size.Width < 120 && !m.conversation.workFocused || m.conversation.pager.open || m.status.State != Live {
		return 0
	}
	next := time.Duration(0)
	now := m.now().Add(m.content.Offset)
	for _, c := range m.workCards() {
		if c.Started.IsZero() || !c.Finished.IsZero() || c.State == protocol.RunningStateDone {
			continue
		}
		until := time.Minute - max(0, now.Sub(c.Started))%time.Minute
		if next == 0 || until < next {
			next = until
		}
	}
	return next
}
