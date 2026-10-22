// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func projectWaiting(payload protocol.Payload) *state.WaitingSummary {
	p, ok := payload.(protocol.ProgressUpdate)
	if !ok || p.Waiting == nil {
		return nil
	}
	summary := &state.WaitingSummary{}
	for _, row := range *p.Waiting {
		line := row.Line.Sentence
		if row.Line.AgentName != "" {
			line = row.Line.AgentName + ": " + line
		}
		// Each Core row occupies one terminal line. References and answer
		// mappings never enter this display projection.
		summary.Lines = append(summary.Lines, strings.Join(strings.Fields(sanitize.Display(line)), " "))
	}
	if p.WaitingMore != nil {
		more, err := strconv.Atoi(string(*p.WaitingMore))
		if err == nil {
			summary.More = max(0, more)
		}
	}
	return summary
}

func (m *shellModel) homeWaiting() *state.WaitingSummary {
	if m.hasCapability("waiting") && m.hasCapability("progress") {
		for _, block := range m.content.Blocks {
			if block.Topic == "progress" && block.Waiting != nil {
				return block.Waiting
			}
		}
	}
	return nil
}

func progressText(payload protocol.Payload, language design.Language, fallback *string) *string {
	p, ok := payload.(protocol.ProgressUpdate)
	if !ok || p.Rows == nil {
		return fallback
	}
	lines := []string{}
	if p.Waiting != nil {
		for _, row := range *p.Waiting {
			lines = append(lines, row.Line.Sentence)
		}
	}
	if p.WaitingMore != nil && *p.WaitingMore != "0" {
		lines = append(lines, fmt.Sprintf(design.Text(language, "progress.more"), string(*p.WaitingMore)))
	}
	for _, row := range *p.Rows {
		v := row.Line
		bar := design.Text(language, "progress.unknown")
		if v.Percent != nil {
			bar = string(*v.Percent) + "%"
		}
		stage := design.Text(language, "progress."+v.Stage)
		if v.Stage != "design" && v.Stage != "building" && v.Stage != "checking" && v.Stage != "done" {
			stage = design.Text(language, "progress.unknown")
		}
		before, next := v.JustFinished, v.Next
		if before == "unknown" {
			before = design.Text(language, "progress.unknown")
		}
		if next == "unknown" {
			next = design.Text(language, "progress.unknown")
		}
		indent := ""
		if v.Depth != "0" {
			indent = "  "
		}
		waiting := ""
		if v.Waiting != "0" {
			waiting = " / " + fmt.Sprintf(design.Text(language, "progress.waiting"), v.Waiting)
		}
		lines = append(lines, fmt.Sprintf(design.Text(language, "progress.line"), indent, v.Name, stage, bar, before, next, waiting))
	}
	if len(lines) == 0 {
		lines = append(lines, design.Text(language, "progress.empty"))
	}
	text := strings.Join(lines, "\n")
	return &text
}
func (m *shellModel) refreshProgress() {
	p := &m.conversation.pager
	if p.open && p.progress {
		p.text = m.topicText("progress")
	}
}

func (m *shellModel) waitingRequest() tea.Cmd {
	if !m.hasCapability("waiting") {
		return nil
	}
	if m.waitingOrder == "" {
		m.waitingOrder = "priority"
	}
	order := m.waitingOrder
	generation := m.generation
	return func() tea.Msg {
		return shellManagement{Generation: generation, Request: protocol.WaitingGet{Order: &order}}
	}
}
func (m *shellModel) waitingText(rows []protocol.WaitingRow) string {
	if len(rows) == 0 {
		return design.Text(m.styles.options.Language, "waiting.empty")
	}
	lines := []string{}
	for _, row := range rows {
		v := row.Line
		line := v.Sentence
		if v.AgentName != "" {
			line = v.AgentName + ": " + line
		}
		if v.Recommendation != "" {
			line += " / " + v.Recommendation
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *shellModel) ownerCommand(text string) (bool, tea.Cmd) {
	kind, request, ok := protocol.OwnerCommand(text, "")
	if !ok || kind == protocol.KindInterrupt {
		return false, nil
	}
	capability := "running"
	if kind == protocol.KindProgressDigestGet {
		capability = "progress"
	}
	if kind == protocol.KindWaitingGet {
		capability = "waiting"
	}
	if !m.hasCapability(capability) {
		m.openPager(design.Text(m.styles.options.Language, "capability_not_negotiated"))
		return true, nil
	}
	if p, ok := request.(protocol.WaitingGet); ok {
		if p.Order == nil {
			order := "priority"
			p.Order = &order
		}
		m.waitingOrder = *p.Order
		m.openPager("")
		m.conversation.pager.waiting = true
		request = p
	}
	generation := m.generation
	return true, func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}
