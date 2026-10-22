// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (m *shellModel) openRunning() {
	m.openPager(m.topicText("running"))
	m.conversation.pager.running = true
	m.refreshRunning()
}

func (m *shellModel) refreshRunning() {
	p := &m.conversation.pager
	if !p.open || !p.running {
		return
	}
	rows := m.topicBlocks("running")
	p.selected = max(0, min(p.selected, len(rows)-1))
	lines := []string{design.Text(m.styles.options.Language, "running.keys"), ""}
	for i, row := range rows {
		prefix := "  "
		if i == p.selected {
			prefix = "> "
		}
		lines = append(lines, prefix+row.Text)
	}
	p.text = strings.Join(lines, "\n")
}

func (m *shellModel) runningRequest(id, action string, offset *json.Number) tea.Cmd {
	if id == "" || !m.mutationsAllowed() {
		return nil
	}
	a := action
	request := protocol.RunningGet{RunID: &id, Action: &a, Offset: offset}
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}
