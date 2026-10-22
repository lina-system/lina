// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type recordPager struct {
	list       state.RecordList
	query      protocol.RecordRequest
	next       *string
	detail     *protocol.RecordRow
	detailNext *string
	preview    bool
	saved      bool
	token      *string
	busy       bool
}

func (m *shellModel) recordsCommand(text string) (bool, tea.Cmd) {
	head, args := cutCommand(text)
	if head != "/records" && head != "/memos" && head != "/add" {
		return false, nil
	}
	m.openPager("")
	if !m.hasCapability("records") {
		m.conversation.pager.text = design.Text(m.styles.options.Language, "capability_not_negotiated")
		return true, nil
	}
	v := &recordPager{}
	m.conversation.pager.records = v
	if head == "/add" {
		if args == "" {
			m.conversation.pager.text = design.Text(m.styles.options.Language, "records.add_help")
			return true, nil
		}
		v.preview = true
		return true, m.recordRequest(protocol.RecordRequest{Operation: "preview", Text: &args})
	}
	view, search := cutCommand(args)
	if view == "" {
		view = "memos"
	}
	if head == "/memos" {
		view = "memos"
		search = args
	}
	switch view {
	case "memos", "today", "upcoming", "anytime", "later", "board", "list", "week", "all":
	default:
		m.conversation.pager.text = design.Text(m.styles.options.Language, "records.help")
		return true, nil
	}
	if view == "all" {
		view = ""
	}
	now := m.now()
	day, zone := now.Format(time.DateOnly), now.Location().String()
	// time.Local.String() is "Local" rather than an IANA identifier. Core accepts
	// the main host's Local zone too; remote clients send their actual zone.
	v.query = protocol.RecordRequest{Operation: "query", View: &view, Search: &search, Day: &day, Zone: &zone}
	return true, m.recordRequest(v.query)
}
func (m *shellModel) recordRequest(request protocol.RecordRequest) tea.Cmd {
	v := m.conversation.pager.records
	if v == nil || v.busy || !m.mutationsAllowed() {
		return nil
	}
	v.busy = true
	m.conversation.pager.text = design.Text(m.styles.options.Language, "records.loading")
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}
func (m *shellModel) recordResponse(request protocol.RecordRequest, result protocol.RecordResult) error {
	p := &m.conversation.pager
	v := p.records
	if v == nil || result.Operation != request.Operation {
		return errStream
	}
	v.busy = false
	if result.Code != nil {
		p.text = design.Text(m.styles.options.Language, "records."+*result.Code)
		return nil
	}
	switch result.Operation {
	case "query":
		v.list = v.list.Replace(result.Rows)
		v.next = result.Next
		v.detail = nil
		v.preview = false
	case "read":
		if len(result.Rows) != 1 || request.Ref == nil || result.Rows[0].Ref != *request.Ref {
			return errStream
		}
		row := state.RecordList{}.Replace(result.Rows).Rows[0]
		if request.After != nil && v.detail != nil {
			if row.Revision != v.detail.Revision {
				return errStream
			}
			row.Note = v.detail.Note + row.Note
			row.Chips = append(v.detail.Chips, row.Chips...)
		}
		v.detail = &row
		v.detailNext = result.Next
	case "preview":
		if len(result.Rows) != 1 || result.Token == nil {
			return errStream
		}
		row := state.RecordList{}.Replace(result.Rows).Rows[0]
		v.detail = &row
		v.preview = true
		v.token = result.Token
	case "save":
		if len(result.Rows) != 1 {
			return errStream
		}
		row := state.RecordList{}.Replace(result.Rows).Rows[0]
		v.detail = &row
		v.preview = false
		v.saved = true
		v.token = nil
	default:
		return errStream
	}
	p.offset = 0
	m.refreshRecords()
	return nil
}
func (m *shellModel) refreshRecords() {
	p := &m.conversation.pager
	v := p.records
	if v == nil {
		return
	}
	text := func(id string) string { return design.Text(m.styles.options.Language, "records."+id) }
	lines := []string{text("heading"), text("keys"), ""}
	if v.detail != nil {
		row := v.detail
		lines = []string{row.Title, m.recordChips(row.Chips), "", row.Note}
		if v.saved {
			lines = append([]string{text("saved")}, lines...)
		}
		if v.preview {
			lines = append([]string{text("preview")}, lines...)
			lines = append(lines, "", text("confirm"))
		}
		if v.detailNext != nil && !v.preview {
			lines = append(lines, "", text("more"))
		}
	} else {
		for i, row := range v.list.Rows {
			mark := "  "
			if i == v.list.Selected {
				mark = "> "
			}
			line := mark + oneLaneLine(row.Title)
			if row.Overdue {
				line += " / " + text("overdue")
			}
			if row.Blocked {
				line += " / " + text("blocked")
			}
			if recordValueUI(v.query.View) == "board" || recordValueUI(v.query.View) == "list" {
				line += " / " + text("state."+row.State)
			}
			width := m.size.Width
			if plan := m.conversationPlan(); plan.SidePane.Width > 0 {
				width = plan.SidePane.Width
			}
			lines = append(lines, render.Truncate(line, max(1, width-2), render.Unicode{}))
		}
		if len(v.list.Rows) == 0 {
			lines = append(lines, text("empty"))
		}
		if v.next != nil {
			lines = append(lines, text("more"))
		}
	}
	p.text = strings.Join(lines, "\n")
	if v.detail == nil {
		height := max(1, m.size.Height-3)
		if plan := m.conversationPlan(); plan.SidePane.Height > 0 {
			height = max(1, plan.SidePane.Height-1)
		}
		line := v.list.Selected + 3
		if line < p.offset {
			p.offset = line
		}
		if line >= p.offset+height {
			p.offset = line - height + 1
		}
	}
}
func recordValueUI(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (m *shellModel) recordChips(chips []protocol.RecordChip) string {
	parts := []string{}
	for _, chip := range chips {
		label := design.Text(m.styles.options.Language, "records.chip."+chip.Kind)
		value := oneLaneLine(chip.Value)
		if value == "" {
			value = design.Text(m.styles.options.Language, "records.none")
		}
		if chip.Kind == "important" {
			value = design.Text(m.styles.options.Language, "records.important")
		}
		parts = append(parts, fmt.Sprintf("[%s: %s]", label, value))
	}
	return strings.Join(parts, " ")
}
func (m *shellModel) recordsKey(name string) (bool, tea.Cmd) {
	p := &m.conversation.pager
	v := p.records
	if v == nil {
		return false, nil
	}
	if name == "esc" && v.detail != nil && !v.preview && !v.busy && v.query.Operation != "" {
		v.detail = nil
		v.detailNext = nil
		p.offset = 0
		m.refreshRecords()
		return true, nil
	}
	if v.busy {
		switch name {
		case "esc", "q", "ctrl+d", "ctrl+o":
			return false, nil
		}
		return true, nil
	}
	if v.preview {
		if name == "enter" && v.token != nil {
			return true, m.recordRequest(protocol.RecordRequest{Operation: "save", Token: v.token})
		}
		return false, nil
	}
	if v.detail != nil {
		if name == "ctrl+n" && v.detailNext != nil {
			return true, m.recordRequest(protocol.RecordRequest{Operation: "read", Ref: &v.detail.Ref, Revision: &v.detail.Revision, After: v.detailNext})
		}
		return false, nil
	}
	switch name {
	case "up", "k":
		v.list = v.list.Move(-1)
	case "down", "j":
		v.list = v.list.Move(1)
	case "enter":
		if len(v.list.Rows) > 0 {
			id := v.list.Rows[v.list.Selected].Ref
			return true, m.recordRequest(protocol.RecordRequest{Operation: "read", Ref: &id})
		}
	case "r":
		return true, m.recordRequest(v.query)
	case "ctrl+n":
		if v.next != nil {
			q := v.query
			q.After = v.next
			return true, m.recordRequest(q)
		}
	default:
		return false, nil
	}
	m.refreshRecords()
	return true, nil
}
