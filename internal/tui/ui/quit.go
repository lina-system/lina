// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
)

const quitConfirmationWindow = 2 * time.Second

// The connection/input loop owns the timer. Keeping its expiry as a message
// lets the model use its injected clock without blocking command dispatch.
type quitHintExpired struct{}

// controlC runs only after the terminal key admission gate accepts a press.
func (m *shellModel) controlC() tea.Cmd {
	if m.content.Draft.Text != "" || len(m.content.Draft.Attachments) != 0 {
		m.quitDeadline = time.Time{}
		return m.reduceInput(state.EditDraft{})
	}
	now := m.now()
	if !m.quitDeadline.IsZero() && now.Before(m.quitDeadline) {
		m.status = ConnectionStatus{State: Quit}
		return tea.Quit
	}
	m.quitDeadline = now.Add(quitConfirmationWindow)
	return nil
}

func (m *shellModel) quitHint() string {
	if m.quitDeadline.IsZero() {
		return ""
	}
	return design.Text(m.styles.options.Language, "shell.quit_hint")
}
