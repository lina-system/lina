// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
)

type workStageView struct {
	card, selected, expanded string
	index                    int
	step                     string
	stepIndex, stepOffset    int
}

func stageKey(stage state.WorkStage) string { return stage.Kind + ":" + stage.Number }

// Keep the opened work independent of the card filter and terminal width.
// Authoritative removal (including capability revocation) drops stale detail.
func (m *shellModel) openedWorkCard() (state.WorkCard, bool) {
	v := &m.conversation.workStages
	if v.card != "" {
		for _, card := range m.workCards() {
			if card.ID == v.card {
				return card, true
			}
		}
		*v = workStageView{}
		m.selectableWorkCards()
	}
	return state.WorkCard{}, false
}

func currentWorkStage(stages []state.WorkStage) int {
	for i, stage := range stages {
		if stage.State != "done" && stage.State != "superseded" {
			return i
		}
	}
	return max(0, len(stages)-1)
}

func stageExpandable(stage state.WorkStage) bool {
	if stage.State == "done" || stage.State == "superseded" || stage.State == "next" {
		return false
	}
	for _, step := range stage.Steps {
		if step.State != "done" && step.State != "superseded" && step.State != "next" &&
			(step.State == "waiting" || step.State == "working" || step.State == "needs_input" || !step.Started.IsZero()) {
			return true
		}
	}
	return false
}

func (m *shellModel) syncWorkStage(card state.WorkCard) {
	v := &m.conversation.workStages
	if len(card.Stages) == 0 {
		v.selected, v.expanded, v.index, v.step = "", "", 0, ""
		return
	}
	index := min(v.index, len(card.Stages)-1)
	if v.selected == "" {
		index = currentWorkStage(card.Stages)
	}
	expanded := ""
	if v.step != "" {
		for _, stage := range card.Stages {
			for _, step := range stage.Steps {
				if step.ID == v.step && stageExpandable(stage) {
					v.selected, v.expanded = stageKey(stage), stageKey(stage)
				}
			}
		}
	}
	for i, stage := range card.Stages {
		if stageKey(stage) == v.selected {
			index = i
		}
		if stageKey(stage) == v.expanded && stageExpandable(stage) {
			expanded = v.expanded
		}
	}
	v.index, v.selected, v.expanded = index, stageKey(card.Stages[index]), expanded
	if expanded == "" {
		v.step, v.stepOffset = "", 0
	} else if v.step != "" {
		steps := navigableSteps(card.Stages[index].Steps)
		if len(steps) == 0 {
			v.step = ""
		} else {
			i := min(v.stepIndex, len(steps)-1)
			for j, step := range steps {
				if step.ID == v.step {
					i = j
					break
				}
			}
			v.step, v.stepIndex = steps[i].ID, i
		}
	}
}

func (m *shellModel) workStageKey(card state.WorkCard, key string) {
	m.syncWorkStage(card)
	v := &m.conversation.workStages
	switch key {
	case "esc":
		if v.expanded != "" {
			v.expanded, v.step, v.stepOffset = "", "", 0
		} else {
			*v = workStageView{}
		}
	case "up", "down":
		if m.moveWorkStep(card, key) {
			return
		}
		if len(card.Stages) > 0 {
			delta := 1
			if key == "up" {
				delta = -1
			}
			v.index = min(max(0, v.index+delta), len(card.Stages)-1)
			v.selected = stageKey(card.Stages[v.index])
		}
	case "enter":
		if v.step != "" {
			return
		}
		if len(card.Stages) > 0 && stageExpandable(card.Stages[v.index]) {
			if v.expanded == v.selected {
				v.expanded = ""
			} else {
				v.expanded = v.selected
				v.stepOffset = 0
			}
		}
	case "ctrl+o":
		m.openWorkDetails()
	}
}

// Panel text stays on one physical line and cannot draw graph edges, including
// when a label supplied by Core contains terminal control or drawing characters.
func panelText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 0x2190 && r <= 0x21ff || r >= 0x2500 && r <= 0x257f {
			return ' '
		}
		return r
	}, sanitize.Display(s))
	return strings.Join(strings.Fields(s), " ")
}

func (m *shellModel) stageMark(status string) string {
	t := m.styles.tokens
	marks := map[string][2]string{
		"done":        {t.GlyphUnicodeStageDone, t.GlyphAsciiStageDone},
		"superseded":  {t.GlyphUnicodeStageDone, t.GlyphAsciiStageDone},
		"working":     {t.GlyphUnicodeStageWorking, t.GlyphAsciiStageWorking},
		"waiting":     {t.GlyphUnicodeStageWaiting, t.GlyphAsciiStageWaiting},
		"needs_input": {t.GlyphUnicodeStageNeedsInput, t.GlyphAsciiStageNeedsInput},
		"failed":      {t.GlyphUnicodeStageFailed, t.GlyphAsciiStageFailed},
		"stopped":     {t.GlyphUnicodeStageFailed, t.GlyphAsciiStageFailed},
		"next":        {t.GlyphUnicodeStageNext, t.GlyphAsciiStageNext},
	}
	mark, ok := marks[status]
	if !ok {
		mark = [2]string{t.GlyphUnicodeUnknown, t.GlyphAsciiUnknown}
	}
	if m.styles.options.ASCII {
		return mark[1]
	}
	return mark[0]
}

func (m *shellModel) workStageRows(card state.WorkCard, width, height int) []string {
	m.syncWorkStage(card)
	v := &m.conversation.workStages
	t := m.styles.tokens
	back, tail, marker := t.GlyphUnicodeBack, t.GlyphUnicodeOverflow, t.GlyphUnicodeAction
	if m.styles.options.ASCII {
		back, tail, marker = t.GlyphAsciiBack, t.GlyphAsciiOverflow, t.GlyphAsciiAction
	}
	clip := func(s string) string { return panelClip(s, width, tail) }
	rows := []string{clip(back + " " + panelText(card.Name))}
	if len(card.Stages) == 0 {
		if height > 1 {
			rows = append(rows, clip(design.Text(m.styles.options.Language, "unknown")))
		}
		return rows
	}
	expanded := -1
	for i, stage := range card.Stages {
		if stageKey(stage) == v.expanded {
			expanded = i
		}
	}
	shown := visibleStages(card.Stages, v.index, expanded, max(0, height-1))
	stepBudget := height - 1 - stageRowCount(shown)
	for i := 0; i < len(card.Stages) && len(rows) < height; {
		if !shown[i] {
			start, done := i, true
			for i < len(shown) && !shown[i] {
				done = done && card.Stages[i].State == "done"
				i++
			}
			line := fmt.Sprintf(design.Text(m.styles.options.Language, "work.more"), tail, i-start)
			if done {
				line = fmt.Sprintf(design.Text(m.styles.options.Language, "work.done"), i-start)
			}
			rows = append(rows, clip(line))
			continue
		}
		stage := card.Stages[i]
		prefix := "  "
		if i == v.index && v.step == "" {
			prefix = marker + " "
		}
		if stage.Number != "" {
			prefix += stage.Number + " "
		}
		rows = append(rows, clip(prefix+m.stageMark(stage.State)+" "+panelText(stage.Label)))
		if i == expanded {
			for _, row := range m.workStepRows(stage.Steps, stepBudget, width, tail) {
				rows = append(rows, clip(row))
			}
		}
		i++
	}
	return rows
}

func stageRowCount(shown []bool) int {
	rows := 0
	for i, visible := range shown {
		if visible || i == 0 || shown[i-1] {
			rows++
		}
	}
	return rows
}

// Fold only for a shortage. Keep the current and selected entries reachable;
// step expansion consumes the remaining rows, with its own exact hidden count.
func visibleStages(stages []state.WorkStage, selected, expanded, budget int) []bool {
	shown := make([]bool, len(stages))
	for i := range shown {
		shown[i] = true
	}
	wanted := len(stages)
	if expanded >= 0 {
		for _, step := range stages[expanded].Steps {
			wanted += stepRowCount(step)
		}
	}
	if wanted <= budget {
		return shown
	}
	current := currentWorkStage(stages)
	protected := func(i int) bool { return i == current || i == selected || i == expanded }
	leading := 0
	for leading < len(stages) && stages[leading].State == "done" {
		leading++
	}
	for i := 0; i < leading-1; i++ {
		shown[i] = protected(i)
	}
	next := false
	for i, stage := range stages {
		if stage.State == "next" {
			if next && !protected(i) {
				shown[i] = false
			}
			next = true
		}
	}
	// Replacing one entry by one count row saves no space and loses its label.
	for i := 0; i < len(shown); {
		if shown[i] {
			i++
			continue
		}
		start := i
		for i < len(shown) && !shown[i] {
			i++
		}
		if i-start == 1 {
			shown[start] = true
		}
	}
	for i := len(stages) - 1; i >= 0 && stageRowCount(shown) > budget; i-- {
		if !protected(i) {
			shown[i] = false
		}
	}
	return shown
}

func (m *shellModel) openWorkDetails() {
	card, open := m.openedWorkCard()
	if !open {
		cards, _ := m.selectableWorkCards()
		if len(cards) == 0 {
			return
		}
		card = cards[m.conversation.workIndex]
	}
	m.openPager("")
	m.conversation.pager.workCard = card.ID
	if open {
		m.syncWorkStage(card)
		m.conversation.pager.workStage = m.conversation.workStages.selected
		m.conversation.pager.workStep = m.conversation.workStages.step
	}
}

func (m *shellModel) workDetails() string {
	p := &m.conversation.pager
	var card state.WorkCard
	found := false
	for _, c := range m.workCards() {
		if c.ID == p.workCard {
			card, found = c, true
			break
		}
	}
	if !found {
		return design.Text(m.styles.options.Language, "unknown")
	}
	lines := []string{card.Name, card.Now}
	if p.workStage != "" {
		m.syncWorkStage(card)
		lines = []string{card.Name, design.Text(m.styles.options.Language, "unknown")}
		for _, stage := range card.Stages {
			if stageKey(stage) == p.workStage {
				lines = []string{card.Name, stage.Label}
				if p.workStep != "" {
					lines = []string{card.Name, design.Text(m.styles.options.Language, "unknown")}
					if stageExpandable(stage) && m.conversation.workStages.expanded == stageKey(stage) {
						for _, step := range stage.Steps {
							if step.ID == p.workStep && step.State != "superseded" {
								lines = []string{card.Name, step.Name, m.stageMark(step.State)}
								for _, worker := range liveStepWorkers(step) {
									lines = append(lines, m.workWorkerLine(worker))
								}
							}
						}
					}
					break
				}
				// Details follows the same disclosure rule as the panel.
				if stageExpandable(stage) && m.conversation.workStages.expanded == stageKey(stage) {
					for _, step := range stage.Steps {
						if step.State != "superseded" {
							lines = append(lines, m.stageMark(step.State)+" "+step.Name)
						}
					}
				}
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
