// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

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
			percent, err := v.Percent.Int64()
			if err == nil && percent >= 0 && percent <= 100 {
				bar = "[" + strings.Repeat("█", int(percent)/10) + strings.Repeat("░", 10-int(percent)/10) + "] " + string(*v.Percent) + "%"
			}
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
			waiting = " · " + fmt.Sprintf(design.Text(language, "progress.waiting"), v.Waiting)
		}
		lines = append(lines, indent+v.Name+" · "+stage+" · "+bar+" · "+before+" → "+next+waiting)
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
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: protocol.WaitingGet{}} }
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
			line += " · " + v.Recommendation
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
