// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
)

// keyBinding is the local reference for implemented conversation shortcuts.
// Lower priorities survive longer. An empty Hint keeps an action sheet-only.
// Keys[0] is the compact footer spelling; the sheet includes all alternatives.
type keyBinding struct {
	Action   string
	Keys     []string
	Priority int
	Hint     string
	Section  string
}

var keyBindings = []keyBinding{
	{"shortcuts.open", []string{"?", "/help"}, 1, "shortcuts.title", ""},
	{"shortcuts.escape", []string{"Esc"}, 2, "shortcuts.stop", ""},
	{"shortcuts.send", []string{"Enter"}, 3, "shortcuts.send", ""},
	{"action.quit", []string{"Ctrl+D"}, 4, "action.quit", ""},
	{"shortcuts.newline", []string{"Alt+Enter", "Shift+Enter", "Ctrl+J"}, 5, "shortcuts.newline", ""},
	{"shortcuts.scroll", []string{"PgUp/PgDn"}, 6, "shortcuts.scroll", ""},
	{"shortcuts.card", []string{"Ctrl+O"}, 7, "shortcuts.open_hint", ""},
	{"shortcuts.decisions", []string{"F6"}, 8, "shortcuts.decisions", ""},
	{"shortcuts.clear", []string{"Ctrl+C"}, 9, "", ""},
	{"shortcuts.focus", []string{"Tab", "Shift+Tab"}, 10, "", ""},
	{"shortcuts.undo", []string{"Ctrl+Z"}, 11, "", ""},
	{"waiting", []string{"Ctrl+W"}, 12, "", ""},
	{"running", []string{"Ctrl+R"}, 13, "", ""},
	{"progress", []string{"Ctrl+P"}, 14, "", ""},
	{"shortcuts.transcript", []string{"Ctrl+T"}, 15, "shortcuts.transcript", ""},
	{"shortcuts.close", []string{"Esc", "q", "Ctrl+O"}, 1, "shortcuts.close", "pager"},
	{"shortcuts.search", []string{"/"}, 2, "shortcuts.search", "pager"},
	{"shortcuts.next", []string{"n"}, 3, "shortcuts.next", "pager"},
	{"shortcuts.scroll", []string{"Up/Down", "PgUp/PgDn", "Home/End"}, 4, "", "pager"},
	{"account.heading", []string{"/accounts"}, 1, "", "commands"},
	{"sign_in_open", []string{"/signin"}, 2, "", "commands"},
	{"memory.heading", []string{"/memory"}, 3, "", "commands"},
	{"agents.heading", []string{"/agents"}, 4, "", "commands"},
	{"channel.heading", []string{"/channels"}, 5, "", "commands"},
	{"talk.heading", []string{"/talk"}, 6, "", "commands"},
	{"interrupt", []string{"/stop"}, 7, "", "commands"},
	{"shortcuts.queue", []string{"/queue <text>"}, 8, "", "commands"},
}

func bindingsFor(section string) []keyBinding {
	var bindings []keyBinding
	for _, binding := range keyBindings {
		if binding.Section == section {
			bindings = append(bindings, binding)
		}
	}
	slices.SortStableFunc(bindings, func(a, b keyBinding) int { return a.Priority - b.Priority })
	return bindings
}

func (m *shellModel) shortcutSheet() string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	var lines []string
	for _, section := range []struct{ id, title string }{{"", "shortcuts.title"}, {"pager", "shortcuts.pager"}, {"commands", "controls.help"}} {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, text(section.title))
		for _, binding := range bindingsFor(section.id) {
			lines = append(lines, strings.Join(binding.Keys, ", ")+"  "+text(binding.Action))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *shellModel) conversationFooter(showNotice bool) string {
	status := m.styles.presentation(m.status)
	line := status.Mark + " " + status.Label
	width := m.size.Width
	section := ""
	if m.conversation.pager.open {
		section = "pager"
	}
	var hints []string
	for _, binding := range bindingsFor(section) {
		if binding.Hint != "" {
			hints = append(hints, binding.Keys[0]+" "+design.Text(m.styles.options.Language, binding.Hint))
		}
	}
	// A notice cannot displace the first shortcut or the connection identity.
	// Quit confirmation is transient input feedback, even when local notices
	// are suppressed. Expiry restores the usual cause or notice and key hints.
	notice := m.quitHint()
	if notice == "" {
		if m.status.Cause != "" {
			notice = status.Detail
		} else if showNotice {
			notice = m.localNotice()
		}
	}
	if notice != "" && len(hints) > 0 {
		budget := width - ansi.StringWidth(line+"  "+hints[0]) - 2
		if budget > 1 {
			line += "  " + ansi.Truncate(notice, budget, "…")
		}
	}
	for _, hint := range hints {
		if ansi.StringWidth(line+"  "+hint) > width {
			break
		}
		line += "  " + hint
	}
	if m.conversation.pager.open && m.conversation.pager.query != "" {
		// Search text is content, not a key hint, and may use the remaining cells.
		budget := width - ansi.StringWidth(line) - 2
		if budget > 0 {
			line += "  " + ansi.Truncate(m.conversation.pager.query, budget, "")
		}
	}
	// Below the supported terminal minimum even the connection may not fit.
	return ansi.Truncate(line, max(0, width), "")
}
