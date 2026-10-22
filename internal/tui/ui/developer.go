// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
)

func (m *shellModel) developerCommand(text string) (bool, tea.Cmd) {
	if text != "/developer" {
		return false, nil
	}
	if !m.hasCapability("worker_lanes") {
		m.openPager(design.Text(m.styles.options.Language, "capability_not_negotiated"))
		return true, nil
	}
	m.openPager("")
	m.conversation.pager.developer = true
	m.refreshDeveloper()
	return true, m.developerRequest(nil)
}
func (m *shellModel) developerRequest(after *string) tea.Cmd {
	if !m.mutationsAllowed() || m.conversation.pager.developerBusy {
		return nil
	}
	m.conversation.pager.developerBusy = true
	generation := m.generation
	return func() tea.Msg {
		return shellManagement{Generation: generation, Request: protocol.ProgressGet{After: after}}
	}
}
func oneLaneLine(s string) string { return strings.Join(strings.Fields(sanitize.Display(s)), " ") }
func (m *shellModel) refreshDeveloper() {
	p := &m.conversation.pager
	if !p.open || !p.developer {
		return
	}
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	width := m.size.Width
	if plan := m.conversationPlan(); plan.SidePane.Width > 0 {
		width = plan.SidePane.Width
	}
	lines := []string{text("developer.heading"), text("developer.keys"), ""}
	// Match the view's navigation order. A repeated persona heading is allowed at
	// page boundaries, but a worker session is never coalesced with its namesake.
	agent := ""

	for _, row := range p.lanes.Rows {
		if row.AgentID != agent {
			agent = row.AgentID
			name := oneLaneLine(row.AgentName)
			initial := "?"
			if r := []rune(name); len(r) > 0 {
				initial = string(r[0])
			}
			lines = append(lines, initial+" "+name)
		}
		mark := "  "
		if row.LaneID == p.lanes.Selected {
			mark = "> "

		}
		fold := "+"
		if p.lanes.Expanded[row.LaneID] {
			fold = "-"
		}
		summary := mark + "  " + fold + " [" + oneLaneLine(row.Worker) + "] → " + oneLaneLine(row.Title) + "  " + text("developer.state."+row.State)
		// A collapsed lane occupies exactly one terminal row, even with long or
		// multiline worker text. Full summaries remain available on expansion.
		lines = append(lines, render.Truncate(summary, max(1, width-2), render.Unicode{}))
		if p.lanes.Expanded[row.LaneID] {
			lines = append(lines, "      "+oneLaneLine(row.Title), "      task: "+oneLaneLine(row.TaskID)+" / "+row.Generation,
				"      thread: "+oneLaneLine(row.ThreadID), "      turn: "+oneLaneLine(row.TurnID), "      node: "+oneLaneLine(row.NodeID))
			for _, detail := range row.Details {
				lines = append(lines, "      "+oneLaneLine(detail))
			}
		}
	}
	if len(p.lanes.Rows) == 0 {
		lines = append(lines, text("developer.empty"))
	}
	if p.laneNext != nil {
		lines = append(lines, text("developer.more"))
	}
	p.text = strings.Join(lines, "\n")
	// Measure the wrapped pager, including headings and expanded details, so
	// selection stays visible in both the overlay and the narrow side pane.
	selectedLine := 0
	for i, line := range m.pagerLines() {
		if strings.HasPrefix(line, ">   ") {
			selectedLine = i
			break
		}
	}
	height := m.size.Height - 1
	if plan := m.conversationPlan(); plan.SidePane.Height > 0 {
		height = plan.SidePane.Height
	}
	if selectedLine < p.offset {
		p.offset = selectedLine
	}
	if selectedLine >= p.offset+max(1, height) {
		p.offset = selectedLine - max(1, height) + 1
	}
}
func (m *shellModel) developerKey(name string) (bool, tea.Cmd) {
	p := &m.conversation.pager
	switch name {
	case "up", "k":
		p.lanes = p.lanes.Move(-1)
	case "down", "j":
		p.lanes = p.lanes.Move(1)
	case "enter":
		p.lanes = p.lanes.Toggle()
	case "r":
		return true, m.developerRequest(nil)
	case "n":
		if p.laneNext != nil {
			return true, m.developerRequest(p.laneNext)
		}
		return true, nil
	default:
		return false, nil
	}
	m.refreshDeveloper()
	return true, nil
}
