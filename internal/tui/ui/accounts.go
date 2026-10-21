// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The callback buffer belongs only to this transient form. It is never a
// conversation draft, undo entry, local echo, notice or persisted preference.
type accountView struct {
	open, busy              bool
	stage, url, input, code string
	selected, offset        int
}
type shellAccountRequest struct {
	Generation uint64
	Request    protocol.AccountRequest
}
type shellRedirect struct {
	Generation uint64
	Attempt    uint64
	Request    protocol.SignInRedirect
}
type shellCancelSignIn struct{ Generation uint64 }

func (m *shellModel) canSignIn() bool {
	return m.hasCapability("accounts") && m.hasCapability("sign_in_relay")
}
func (m *shellModel) hasComposer() bool {
	return m.hasCapability("conversation") || m.hasCapability("accounts") || m.hasCapability("agents") || m.hasCapability("memory")
}

func (m *shellModel) startSignIn(purpose string) tea.Cmd {
	m.accounts = accountView{open: true, busy: true, stage: "sign_in_listen"}
	if !m.canSignIn() {
		m.accounts.busy, m.accounts.code = false, "capability_not_negotiated"
		return nil
	}
	generation := m.generation
	return func() tea.Msg {
		return shellAccountRequest{Generation: generation, Request: protocol.AccountRequest{
			Operation: stringPointer("sign_in"), Provider: stringPointer("chatgpt"), Purpose: &purpose,
		}}
	}
}

func (m *shellModel) accountCommand(text string) (bool, tea.Cmd) {
	switch text {
	case "/signin":
		return true, m.startSignIn("login")
	case "/accounts":
		return true, m.listAccounts()
	}
	return false, nil
}

func (m *shellModel) accountPaste(text string) {
	if m.accounts.stage == "sign_in_open" && !m.accounts.busy && len(text) <= 16384 {
		m.accounts.input = text
	}
}

func (m *shellModel) accountKey(msg tea.KeyMsg) tea.Cmd {
	key := term.FromTea(msg, m.events)
	class := term.Edit
	if key.Name == "enter" || key.Name == "esc" || key.Name == "ctrl+c" || key.Name == "ctrl+d" || m.accounts.stage != "sign_in_open" {
		class = term.Toggle
	}
	if !m.gate.Admit(key, class, m.now()) {
		return nil
	}
	switch key.Name {
	case "pgup":
		m.accounts.offset = max(0, m.accounts.offset-max(1, m.size.Height-2))
		return nil
	case "pgdown":
		m.accounts.offset += max(1, m.size.Height-2)
		return nil
	case "ctrl+d":
		m.status.State = Quit
		return tea.Quit
	case "esc", "ctrl+c":
		m.accounts = accountView{}
		generation := m.generation
		return func() tea.Msg { return shellCancelSignIn{Generation: generation} }
	}
	if !m.mutationsAllowed() || m.accounts.busy {
		return nil
	}
	if m.accounts.stage != "sign_in_open" {
		if key.Name == "up" {
			m.accounts.selected = max(0, m.accounts.selected-1)
			m.accounts.offset = max(0, m.accounts.selected-max(1, m.size.Height-6))
			return nil
		}
		if key.Name == "down" {
			m.accounts.selected = min(max(0, len(m.accountRows)-1), m.accounts.selected+1)
			m.accounts.offset = max(0, m.accounts.selected-max(1, m.size.Height-6))
			return nil
		}
		if key.Name == "x" && len(m.accountRows) > 0 {
			id := m.accountRows[m.accounts.selected].AccountID
			m.accounts.busy = true
			return m.accountRequest(protocol.AccountRequest{Operation: stringPointer("sign_out"), AccountID: &id})
		}
		if key.Name == "s" {
			if !m.canSignIn() {
				m.accounts.code = "capability_not_negotiated"
				return nil
			}
			if len(m.accountRows) > 0 {
				id := m.accountRows[m.accounts.selected].AccountID
				m.accounts = accountView{open: true, busy: true, stage: "sign_in_listen"}
				return m.accountRequest(protocol.AccountRequest{Operation: stringPointer("sign_in"), Provider: stringPointer("chatgpt"), Purpose: stringPointer("provider"), AccountID: &id})
			}
			return m.startSignIn("login")
		}
		if key.Name == "a" {
			return m.startSignIn("provider")
		}
		return nil
	}
	switch key.Name {
	case "enter":
		if m.accounts.input == "" {
			return nil
		}
		callback := m.accounts.input
		m.accounts.input = ""
		m.accounts.busy, m.accounts.stage = true, "sign_in_redirect"
		generation := m.generation
		return func() tea.Msg {
			return shellRedirect{Generation: generation, Request: protocol.SignInRedirect{URL: &callback}}
		}
	case "backspace":
		runes := []rune(m.accounts.input)
		if len(runes) > 0 {
			m.accounts.input = string(runes[:len(runes)-1])
		}
	default:
		if len(m.accounts.input)+len(msg.Key().Text) <= 16384 {
			m.accounts.input += msg.Key().Text
		}
	}
	return nil
}

func (m *shellModel) accountContent() string {
	text := func(id string) string { return design.Text(m.styles.options.Language, id) }
	lines := []string{text("account.heading"), text("account.empty")}
	if len(m.accountRows) > 0 {
		lines[1] = text("account.summary")
	}
	if m.accounts.stage == "" {
		for i, row := range m.accountRows {
			marker := "  "
			if i == m.accounts.selected {
				marker = "> "
			}
			label := sanitize.Display(row.Label)
			if row.Plan != nil {
				label += " " + sanitize.Display(*row.Plan)
			}
			lines = append(lines, marker+label+"  "+text(row.Status))
		}
	}
	if m.accounts.stage != "" {
		lines[1] = design.Text(m.styles.options.Language, m.accounts.stage)
	}
	if m.accounts.code != "" {
		lines = append(lines, design.Text(m.styles.options.Language, m.accounts.code))
	}
	if m.accounts.url != "" {
		lines = append(lines, ansi.Wrap(sanitize.Display(m.accounts.url), max(1, m.size.Width), ""))
	}
	if m.accounts.stage == "sign_in_open" {
		lines = append(lines, text("account.paste")+"  Enter", strings.Repeat("*", min(40, len(m.accounts.input))))
	} else if !m.accounts.busy {
		lines = append(lines, text("account.help"))
	}
	rows := strings.Split(strings.Join(lines, "\n"), "\n")
	available := max(0, m.size.Height-1)
	m.accounts.offset = min(m.accounts.offset, max(0, len(rows)-available))
	rows = rows[m.accounts.offset:min(len(rows), m.accounts.offset+available)]
	rows = append(rows, "Esc  Ctrl+D  PgUp/PgDown")
	return strings.Join(rows, "\n")
}

func (m *shellModel) accountRequest(request protocol.AccountRequest) tea.Cmd {
	generation := m.generation
	return func() tea.Msg { return shellAccountRequest{Generation: generation, Request: request} }
}
func (m *shellModel) listAccounts() tea.Cmd {
	m.accounts = accountView{open: true, busy: true}
	if !m.hasCapability("accounts") {
		m.accounts.busy = false
		m.accounts.code = "capability_not_negotiated"
		return nil
	}
	return m.accountRequest(protocol.AccountRequest{Operation: stringPointer("list")})
}
