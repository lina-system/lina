// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
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
		// A 목표 heading and the rows inside a line are drawn by their line.
		if v.Kind == "goal" || v.Row != nil && *v.Row {
			continue
		}
		c := state.WorkCard{ID: row.Ref, Name: clean(v.Name)}
		if row.GoalID != nil {
			c.GoalID = *row.GoalID
		}
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
		// Core's pause belongs to the card, not to its Now text: the person sees
		// the paused word and the same card answers resume with its own state.
		if v.Paused != nil && *v.Paused {
			c.Paused = true
		}
		if v.Stages != nil {
			refs := map[string][]string{}
			if row.StageRefs != nil {
				for _, ref := range *row.StageRefs {
					number := ""
					if ref.Number != nil {
						number = ref.Number.String()
					}
					refs[ref.Kind+":"+number] = ref.Steps
				}
			}
			for _, entry := range *v.Stages {
				stage := state.WorkStage{Kind: entry.Kind, Label: panelText(entry.Label), State: entry.State}
				if entry.Number != nil {
					stage.Number = entry.Number.String()
				}
				if entry.Steps != nil {
					for i, step := range *entry.Steps {
						value := state.WorkStageStep{Name: panelText(step.Name), State: step.State}
						if ids := refs[stageKey(stage)]; i < len(ids) {
							value.ID = ids[i]
						}
						if step.Workers != nil {
							for _, w := range *step.Workers {
								value.Workers = append(value.Workers, state.WorkStageWorker{Name: panelText(w.Name), State: w.State})
							}
						}
						if step.StartedAt != nil {
							value.Started, _ = time.Parse(time.RFC3339Nano, *step.StartedAt)
						}
						stage.Steps = append(stage.Steps, value)
					}
				}
				c.Stages = append(c.Stages, stage)
			}
		}
		c.Waiting, _ = strconv.Atoi(string(v.Waiting))
		cards = append(cards, c)
	}
	if p.CardOrder != nil {
		byID := make(map[string]state.WorkCard, len(cards))
		for _, card := range cards {
			byID[card.ID] = card
		}
		ordered := make([]state.WorkCard, 0, len(cards))
		for _, id := range *p.CardOrder {
			if card, ok := byID[id]; ok {
				ordered = append(ordered, card)
				delete(byID, id)
			}
		}
		// An older or partial projection keeps all remaining cards in row order.
		for _, card := range cards {
			if _, ok := byID[card.ID]; ok {
				ordered = append(ordered, card)
				delete(byID, card.ID)
			}
		}
		cards = ordered
	}
	summary := &state.WorkSummary{Cards: cards}
	// The running count follows Core's lines; it never counts these rows.
	if p.Counts != nil {
		running, _ := p.Counts.Running.Int64()
		waiting, _ := p.Counts.Waiting.Int64()
		summary.Counts = &state.WorkCount{Running: max(0, int(running)), Waiting: max(0, int(waiting))}
	}
	// The title shows a limit only when the person set one; automatic is the
	// absence of a choice, not a number to draw.
	if p.Limit != nil && !p.Limit.Automatic {
		if value, err := p.Limit.Value.Int64(); err == nil && value > 0 {
			summary.Limit = &state.WorkLimit{Value: int(value)}
		}
	}
	return summary
}

// workLimitValue is the run limit Core applied, when the person set one.
func (m *shellModel) workLimitValue() int {
	if !m.hasCapability("progress") {
		return 0
	}
	for _, b := range m.content.Blocks {
		if b.Topic == "progress" && b.Work != nil && b.Work.Limit != nil {
			return b.Work.Limit.Value
		}
	}
	return 0
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

// Navigation and rendering share the same sequence; folded done cards have no
// selectable row. Keep the selected ID across updates, or its former position
// when that card leaves the sequence.
func (m *shellModel) selectableWorkCards() (active []state.WorkCard, done int) {
	for _, c := range m.workCards() {
		if !matchesWorkFilter(c, m.conversation.workFilter) {
			continue
		}
		if c.State == protocol.RunningStateDone && c.Waiting == 0 {
			done++
			continue
		}
		active = append(active, c)
	}
	v := &m.conversation
	if len(active) == 0 {
		v.workSelected, v.workIndex, v.workOffset = "", 0, 0
		return active, done
	}
	index := min(v.workIndex, len(active)-1)
	for i, c := range active {
		if c.ID == v.workSelected {
			index = i
			break
		}
	}
	v.workSelected, v.workIndex = active[index].ID, index
	return active, done
}

var workFilters = []protocol.RunningState{"", protocol.RunningStateNeedsInput, protocol.RunningStateInProgress, protocol.RunningStateWaiting, protocol.RunningStateDone}

func matchesWorkFilter(c state.WorkCard, filter protocol.RunningState) bool {
	if filter == "" {
		return true
	}
	if c.Waiting > 0 || c.State == protocol.RunningStateNeedsInput {
		return filter == protocol.RunningStateNeedsInput
	}
	return c.State == filter
}

func (m *shellModel) workPanelRows(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}
	if card, ok := m.openedWorkCard(); ok {
		return m.workStageRows(card, width, height)
	}
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	ellipsis := "…"
	if m.styles.options.ASCII {
		ellipsis = "..."
	}
	title := text("work.title")
	separator := m.styles.tokens.GlyphUnicodeActivitySeparator
	if m.styles.options.ASCII {
		separator = m.styles.tokens.GlyphAsciiActivitySeparator
	}
	if m.conversation.workFilter != "" {
		title += " " + separator + " " + text("work.filter."+string(m.conversation.workFilter))
	}
	// The limit is shown only when the person set a number; automatic is the
	// absence of a choice.
	if limit := m.workLimitValue(); limit > 0 {
		title += " " + separator + " " + fmt.Sprintf(text("work.limit"), limit)
	}
	rows := []string{panelClip(title, width, ellipsis)}
	active, done := m.selectableWorkCards()
	available := height - 1
	if done > 0 {
		available--
	}
	cardRows := 2
	if m.conversation.workCompact {
		cardRows = 1
	}
	shown := min(len(active), max(0, available/cardRows))
	if shown < len(active) {
		shown = min(len(active), max(0, (available-1)/cardRows))
	}
	start := min(m.conversation.workOffset, max(0, len(active)-shown))
	if shown > 0 {
		start = min(start, m.conversation.workIndex)
		start = max(start, m.conversation.workIndex-shown+1)
	}
	m.conversation.workOffset = start
	marker := m.styles.tokens.GlyphUnicodeAction
	if m.styles.options.ASCII {
		marker = m.styles.tokens.GlyphAsciiAction
	}
	now := m.now().Add(m.content.Offset)
	for _, c := range active[start : start+shown] {
		elapsed := text("unknown")
		if !c.Started.IsZero() {
			end := now
			if !c.Finished.IsZero() {
				end = c.Finished
			}
			elapsed = workElapsed(end.Sub(c.Started))
		}
		prefix := ""
		if m.conversation.workFocused {
			prefix = strings.Repeat(" ", (render.Unicode{}).Width(marker)+1)
			if c.ID == m.conversation.workSelected {
				prefix = marker + " "
			}
		}
		activity := c.Now
		if activity == "" {
			activity = text("unknown")
		}
		// A stop ask replaces the card's Now with its one question until the
		// person confirms or cancels.
		if m.conversation.workStop == c.ID {
			activity = text("work.stop_confirm")
		}
		u := render.Unicode{}
		budget := max(0, width-u.Width(prefix)-u.Width(elapsed)-1)
		nameBudget := budget
		if m.conversation.workCompact {
			// Preserve a readable piece of Now even for a very long name.
			minimumNow := min(u.Width(activity), 8, max(0, (budget-1)/2))
			nameBudget = min(u.Width(c.Name), max(0, budget-minimumNow-1))
		}
		body := prefix + panelClip(c.Name, nameBudget, ellipsis)
		if m.conversation.workCompact {
			body += " " + panelClip(activity, max(0, budget-nameBudget-1), ellipsis)
		}
		first := body + strings.Repeat(" ", max(1, width-u.Width(body)-u.Width(elapsed))) + elapsed
		rows = append(rows, first)
		if !m.conversation.workCompact {
			rows = append(rows, "  "+panelClip(activity, max(0, width-2), ellipsis))
		}
	}
	if done > 0 && len(rows) < height {
		rows = append(rows, fmt.Sprintf(text("work.done"), done))
	}
	if shown < len(active) && len(rows) < height {
		waiting := 0
		for i, c := range active {
			if (i < start || i >= start+shown) && (c.Waiting > 0 || c.State == protocol.RunningStateNeedsInput) {
				waiting++
			}
		}
		more := fmt.Sprintf(text("work.more"), ellipsis, len(active)-shown)
		if waiting > 0 {
			separator := m.styles.tokens.GlyphUnicodeActivitySeparator
			if m.styles.options.ASCII {
				separator = m.styles.tokens.GlyphAsciiActivitySeparator
			}
			more += " " + separator + " " + fmt.Sprintf(text("work.needs"), waiting)
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
	if card, ok := m.openedWorkCard(); ok && key != "ctrl+c" && key != "ctrl+d" {
		m.workStageKey(card, key)
		return nil
	}
	// A stop ask owns the next key: Enter confirms it and any other key
	// cancels it without doing that key's own work.
	if m.conversation.workStop != "" {
		card := m.conversation.workStop
		m.conversation.workStop = ""
		if key == "enter" {
			for _, c := range m.workCards() {
				if c.ID == card {
					return m.workControl(c.GoalID, "stop", true)
				}
			}
			return nil
		}
		if key == "ctrl+d" {
			m.status = ConnectionStatus{State: Quit}
			return tea.Quit
		}
		return nil
	}
	switch key {
	case "ctrl+d":
		m.status = ConnectionStatus{State: Quit}
		return tea.Quit
	case "ctrl+c":
		return m.controlC()
	case "esc":
		m.conversation.workFocused = false
	case "enter":
		cards, _ := m.selectableWorkCards()
		if len(cards) > 0 {
			m.conversation.workStages = workStageView{card: cards[m.conversation.workIndex].ID}
			m.syncWorkStage(cards[m.conversation.workIndex])
		}
	case "ctrl+o":
		m.openWorkDetails()
	case "ctrl+p":
		m.conversation.workCompact = !m.conversation.workCompact
	case "left", "right":
		for i, filter := range workFilters {
			if filter == m.conversation.workFilter {
				delta := 1
				if key == "left" {
					delta = -1
				}
				m.conversation.workFilter = workFilters[(i+delta+len(workFilters))%len(workFilters)]
				m.conversation.workOffset = 0
				m.selectableWorkCards()
				break
			}
		}
	case "up", "down":
		cards, _ := m.selectableWorkCards()
		index := m.conversation.workIndex
		if len(cards) > 0 {
			if key == "up" {
				index = max(0, index-1)
			} else {
				index = min(len(cards)-1, index+1)
			}
			m.conversation.workSelected = cards[index].ID
			m.conversation.workIndex = index
		}
	case "shift+up", "shift+down":
		// A move changes only Core's rank; the card moves when Core's update
		// arrives.
		if card, ok := m.selectedGoalCard(); ok {
			operation := "rank_up"
			if key == "shift+down" {
				operation = "rank_down"
			}
			return m.workControl(card.GoalID, operation, false)
		}
	case "space":
		// The person's pause toggles the card's own recorded state.
		if card, ok := m.selectedGoalCard(); ok {
			operation := "pause"
			if card.Paused {
				operation = "resume"
			}
			return m.workControl(card.GoalID, operation, false)
		}
	case "delete":
		if card, ok := m.selectedGoalCard(); ok {
			m.conversation.workStop = card.ID
		}
	case "-":
		return m.workLimit("limit_down", nil)
	case "+", "shift++", "shift+=":
		return m.workLimit("limit_up", nil)
	case "0":
		return m.workLimit("limit_auto", nil)
	}
	return nil
}

// selectedGoalCard is the selected card when Core names its goal. A background
// run or a conversation worker has no goal, so goal commands skip it.
func (m *shellModel) selectedGoalCard() (state.WorkCard, bool) {
	cards, _ := m.selectableWorkCards()
	if len(cards) == 0 || cards[m.conversation.workIndex].GoalID == "" {
		return state.WorkCard{}, false
	}
	return cards[m.conversation.workIndex], true
}

// workControl sends one of the person's choices about a card. The panel changes
// nothing locally: the card moves, pauses or leaves only when Core's update
// arrives.
func (m *shellModel) workControl(goalID, operation string, confirm bool) tea.Cmd {
	if goalID == "" || !m.mutationsAllowed() || !m.hasCapability("work") {
		return nil
	}
	request := protocol.WorkControlRequest{GoalID: goalID, Operation: operation}
	if confirm {
		value := true
		request.Confirm = &value
	}
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}

// workLimit sends the person's run limit choice. The title shows the applied
// limit only after Core reports it.
func (m *shellModel) workLimit(operation string, value *json.Number) tea.Cmd {
	if !m.mutationsAllowed() || !m.hasCapability("work") {
		return nil
	}
	request := protocol.WorkLimitRequest{Operation: operation, Value: value}
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
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
