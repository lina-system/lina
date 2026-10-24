// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type toolGroup struct{ calls []state.ToolCall }

func (m *shellModel) toolGroups() []toolGroup {
	var groups []toolGroup
	for _, call := range m.content.ToolCalls {
		if len(groups) > 0 {
			last := &groups[len(groups)-1]
			previous := last.calls[len(last.calls)-1]
			if previous.Name == call.Name && previous.Topic == call.Topic && previous.Turn == call.Turn && previous.Anchor == call.Anchor {
				last.calls = append(last.calls, call)
				continue
			}
		}
		groups = append(groups, toolGroup{calls: []state.ToolCall{call}})
	}
	return groups
}
func (m *shellModel) toolGroupRows(group toolGroup, width int) []string {
	call := group.calls[0]
	code := "ok"
	failed, running := 0, 0
	for _, c := range group.calls {
		if c.Status == protocol.ToolActivityStatusStarted {
			running++
		}
		if c.Outcome == protocol.ToolActivityOutcomeFailed {
			failed++
		}
	}
	if failed > 0 {
		code = "failed"
	}
	if running > 0 {
		code = "running"
	}
	icon := "success"
	if code == "running" {
		icon = "working"
	}
	if code == "failed" {
		icon = "error"
	}
	mark := m.styles.tokens.Icon(icon, m.styles.options.ASCII)
	count := "×"
	separator := m.styles.tokens.GlyphUnicodeActivitySeparator
	if m.styles.options.ASCII {
		count = "x"
		separator = m.styles.tokens.GlyphAsciiActivitySeparator
	}
	text := func(key string) string { return design.Text(m.styles.options.Language, "tool."+key) }
	label := mark + " " + text(code) + "  " + call.Name
	if len(group.calls) > 1 {
		label += fmt.Sprintf(" %s%d", count, len(group.calls))
	}
	if running > 0 && failed > 0 {
		label += fmt.Sprintf(" (%d %s)", failed, text("failed"))
	}
	if call.Target != "" {
		label += "  " + call.Target
	}
	rows := []string{render.Truncate(label, width, render.Unicode{})}
	var output []string
	for _, c := range group.calls {
		if c.Summary != "" {
			stream := render.NewStream(c.ID, render.Config{Width: max(1, width-2)}, render.Unicode{})
			stream.Seal(1, c.Summary)
			for i := 0; i < stream.Len(); i++ {
				output = append(output, stream.At(i).Text)
			}
		}
	}
	for _, line := range output[:min(3, len(output))] {
		rows = append(rows, "  "+line)
	}
	if len(output) > 3 {
		rows = append(rows, render.Truncate("  "+fmt.Sprintf(text("more"), len(output)-3)+" "+separator+" Ctrl+O", width, render.Unicode{}))
	}
	return rows
}

// Ctrl+O follows selection when available; otherwise the latest tool summary
// competes with result cards by visible order, preserving the existing pager.
func (m *shellModel) toolSummary() string {
	selected := m.content.Selected
	for i := len(m.content.ToolCalls) - 1; i >= 0; i-- {
		call := m.content.ToolCalls[i]
		if call.Summary == "" || selected != "" && selected != call.ID {
			continue
		}
		if selected == "" {
			if card, ok := m.selectedCard(); ok {
				after := call.Anchor == ""
				for _, b := range m.content.Blocks {
					if after && b.ID == card.ID {
						return ""
					}
					if b.ID == call.Anchor {
						after = true
					}
				}
			}
		}
		var summaries []string
		if selected == "" {
			for _, entry := range m.content.ToolCalls {
				if entry.Summary != "" {
					summaries = append(summaries, entry.Name+"\n"+entry.Summary)
				}
			}
			return strings.Join(summaries, "\n\n")
		}
		for _, g := range m.toolGroups() {
			for _, c := range g.calls {
				if c.ID == call.ID && c.Turn == call.Turn {
					for _, entry := range g.calls {
						if entry.Summary != "" {
							summaries = append(summaries, entry.Name+"\n"+entry.Summary)
						}
					}
					return strings.Join(summaries, "\n\n")
				}
			}
		}
	}
	return ""
}
