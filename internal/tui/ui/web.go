// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
)

// A credential form never shares the composer, history or undo buffers.
type webView struct {
	open, busy, editing, configured bool
	input, code                     string
	attempt                         uint64
}
type shellWebRequest struct {
	Generation, Attempt uint64
	Request             protocol.WebRequest
}

func (m *shellModel) webCommand(text string) (bool, tea.Cmd) {
	if text != "/web" {
		return false, nil
	}
	m.web = webView{open: true, attempt: m.web.attempt + 1}
	if !m.hasCapability("web") {
		m.web.code = "capability_not_negotiated"
		return true, nil
	}
	return true, m.webRequest(protocol.WebRequest{Operation: "status"})
}
func (m *shellModel) webRequest(p protocol.WebRequest) tea.Cmd {
	m.web.busy = true
	m.web.code = ""
	generation, attempt := m.generation, m.web.attempt
	return func() tea.Msg { return shellWebRequest{generation, attempt, p} }
}
func (m *shellModel) webPaste(text string) {
	if m.web.editing && !m.web.busy && len(text) <= 512 {
		m.web.input = text
	}
}
func (m *shellModel) webKey(msg tea.KeyMsg) tea.Cmd {
	v := &m.web
	key := term.FromTea(msg, m.events)
	class := term.Toggle
	if v.editing && key.Name != "enter" && key.Name != "esc" && key.Name != "ctrl+c" && key.Name != "ctrl+d" {
		class = term.Edit
	}
	if !m.gate.Admit(key, class, m.now()) {
		return nil
	}
	switch key.Name {
	case "ctrl+d":
		v.input = ""
		m.status.State = Quit
		return tea.Quit
	case "esc", "ctrl+c":
		*v = webView{attempt: v.attempt + 1}
		return nil
	}
	if !m.mutationsAllowed() || v.busy || !m.hasCapability("web") {
		return nil
	}
	if v.editing {
		switch key.Name {
		case "enter":
			input := strings.TrimSpace(v.input)
			v.input = ""
			if input == "" {
				return nil
			}
			v.editing = false
			return m.webRequest(protocol.WebRequest{Operation: "set", Key: &input})
		case "backspace":
			r := []rune(v.input)
			if len(r) > 0 {
				v.input = string(r[:len(r)-1])
			}
		default:
			if len(v.input)+len(msg.Key().Text) <= 512 {
				v.input += msg.Key().Text
			}
		}
		return nil
	}
	switch key.Name {
	case "s":
		v.editing = true
		v.input = ""
	case "x":
		return m.webRequest(protocol.WebRequest{Operation: "clear"})
	case "r":
		return m.webRequest(protocol.WebRequest{Operation: "status"})
	}
	return nil
}
func (m *shellModel) webContent() string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	if m.web.busy {
		return ansi.Wrap(strings.Join([]string{text("web.heading"), text("web.loading"), "Esc  Ctrl+D"}, "\n"), max(1, m.size.Width), "")
	}
	status := "web.unconfigured"
	if m.web.configured {
		status = "web.configured"
	}
	lines := []string{text("web.heading"), text(status), text("web.help")}
	if m.web.editing {
		lines = append(lines, text("web.key"), strings.Repeat("*", min(40, len(m.web.input))))
	}
	if m.web.code != "" {
		lines = append(lines, text(m.web.code))
	}
	lines = append(lines, "Esc  Ctrl+D")
	return ansi.Wrap(strings.Join(lines, "\n"), max(1, m.size.Width), "")
}
