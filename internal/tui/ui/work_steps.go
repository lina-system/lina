// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"slices"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
)

func orderedWorkSteps(steps []state.WorkStageStep) []state.WorkStageStep {
	steps = slices.DeleteFunc(slices.Clone(steps), func(s state.WorkStageStep) bool { return s.State == "superseded" })
	rank := func(s state.WorkStageStep) int {
		switch s.State {
		case "needs_input":
			return 0
		case "working":
			return 1
		case "waiting":
			return 2
		case "done":
			return 4
		default:
			return 3
		}
	}
	slices.SortStableFunc(steps, func(a, b state.WorkStageStep) int { return rank(a) - rank(b) })
	return steps
}

func navigableSteps(steps []state.WorkStageStep) []state.WorkStageStep {
	return slices.DeleteFunc(orderedWorkSteps(steps), func(s state.WorkStageStep) bool { return s.ID == "" })
}

func liveStepWorkers(step state.WorkStageStep) []state.WorkStageWorker {
	if step.State == "working" || step.State == "needs_input" {
		return step.Workers
	}
	return nil
}

func stepRowCount(step state.WorkStageStep) int {
	if step.State == "superseded" {
		return 0
	}
	return 1 + len(liveStepWorkers(step))
}

func (m *shellModel) moveWorkStep(card state.WorkCard, key string) bool {
	v := &m.conversation.workStages
	if len(card.Stages) == 0 {
		return false
	}
	if v.expanded == v.selected {
		steps := navigableSteps(card.Stages[v.index].Steps)
		if len(steps) > 0 {
			if v.step == "" && key == "down" {
				v.step, v.stepIndex = steps[0].ID, 0
				return true
			}
			if v.step != "" {
				if key == "up" {
					if v.stepIndex == 0 {
						v.step = ""
						return true
					}
					v.stepIndex--
				} else {
					v.stepIndex++
				}
				if v.stepIndex < len(steps) {
					v.step = steps[v.stepIndex].ID
					return true
				}
				v.step = ""
			}
		}
	}
	if key == "up" && v.index > 0 && stageKey(card.Stages[v.index-1]) == v.expanded {
		steps := navigableSteps(card.Stages[v.index-1].Steps)
		if len(steps) > 0 {
			v.index--
			v.selected = v.expanded
			v.stepIndex = len(steps) - 1
			v.step = steps[v.stepIndex].ID
			return true
		}
	}
	return false
}

func (m *shellModel) workWorkerLine(w state.WorkStageWorker) string {
	return "[" + panelText(w.Name) + "] " + design.Text(m.styles.options.Language, "running.state."+string(w.State))
}

// A step and its workers occupy one viewport item. Scrolling can never leave
// an orphaned worker row. Counts include steps hidden before and after it.
func (m *shellModel) workStepRows(steps []state.WorkStageStep, budget, width int, tail string) []string {
	if budget <= 0 {
		return nil
	}
	steps = orderedWorkSteps(steps)
	if len(steps) == 0 {
		return nil
	}
	v := &m.conversation.workStages
	selected := 0
	for i, step := range steps {
		if step.ID != "" && step.ID == v.step {
			selected = i
			break
		}
	}
	start := min(v.stepOffset, selected, len(steps)-1)
	end := start
	for {
		used := 0
		end = start
		for end < len(steps) {
			extra := stepRowCount(steps[end])
			fold := 0
			if start > 0 || end+1 < len(steps) {
				fold = 1
			}
			if used+extra+fold > budget {
				break
			}
			used += extra
			end++
		}
		if selected < end || start == selected {
			break
		}
		start++
	}
	v.stepOffset = start
	marker := m.styles.tokens.GlyphUnicodeAction
	if m.styles.options.ASCII {
		marker = m.styles.tokens.GlyphAsciiAction
	}
	rows := []string{}
	for _, step := range steps[start:end] {
		prefix := "    "
		if step.ID != "" && step.ID == v.step {
			prefix = "  " + marker + " "
		}
		rows = append(rows, prefix+m.stageMark(step.State)+" "+panelText(step.Name))
		for _, worker := range liveStepWorkers(step) {
			word := design.Text(m.styles.options.Language, "running.state."+string(worker.State))
			name := panelClip(worker.Name, max(0, width-11-(render.Unicode{}).Width(word)), tail)
			rows = append(rows, "        ["+name+"] "+word)
		}
	}
	if end-start < len(steps) {
		rows = append(rows, "    "+fmt.Sprintf(design.Text(m.styles.options.Language, "work.more"), tail, len(steps)-(end-start)))
	}
	return rows
}
