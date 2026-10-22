// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestControlCConfirmation(t *testing.T) {
	for _, composer := range []bool{false, true} {
		for _, elapsed := range []time.Duration{0, 2*time.Second - 1, 2 * time.Second, 3 * time.Second} {
			t.Run(fmt.Sprintf("composer=%t/elapsed=%s", composer, elapsed), func(t *testing.T) {
				now := time.Time{}
				m := newShell(Options{Width: 80}, func() time.Time { return now })
				if composer {
					m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
					m.status.State = Live
					m.content.TurnActive = true
				}
				press := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
				_, cmd := m.Update(press)
				if cmd != nil || m.status.State == Quit {
					t.Fatal("first empty Ctrl+C quit instead of showing confirmation")
				}
				hint, ok := design.Lookup(design.English, "shell.quit_hint")
				if !ok || !strings.Contains(m.View().Content, hint) {
					t.Fatal("first empty Ctrl+C did not show the localized hint")
				}
				if composer && !strings.Contains(strings.Split(m.View().Content, "\n")[m.conversationPlan().Status.Y], hint) {
					t.Fatal("confirmation hint is not in the conversation status row")
				}
				now = now.Add(elapsed)
				_, cmd = m.Update(press)
				if elapsed < 2*time.Second {
					if cmd == nil || m.status.State != Quit {
						t.Fatal("second Ctrl+C inside window did not quit")
					}
					if _, ok := cmd().(tea.QuitMsg); !ok {
						t.Fatal("missing quit effect")
					}
				} else if cmd != nil || m.status.State == Quit || !strings.Contains(m.View().Content, hint) {
					t.Fatal("expired confirmation did not start a fresh hint")
				}
			})
		}
	}
}

func TestControlCDraftClearDisarmsConfirmation(t *testing.T) {
	m := conversationShell()
	m.now = func() time.Time { return time.Unix(100, 0) }
	press := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m.Update(press)
	m.reduceInput(state.EditDraft{Draft: state.Draft{Text: "pending", Attachments: []string{"asset"}}})
	_, cmd := m.Update(press)
	if cmd != nil || m.status.State == Quit || m.content.Draft.Text != "" || len(m.content.Draft.Attachments) != 0 {
		t.Fatal("Ctrl+C must clear the whole draft before considering quit")
	}
	_, cmd = m.Update(press)
	if cmd != nil || m.status.State == Quit {
		t.Fatal("clearing a draft must not arm or retain quit confirmation")
	}
}

func TestControlCHintExpiry(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			now := time.Unix(100, 0)
			m := newShell(Options{Width: 80, Language: language}, func() time.Time { return now })
			press := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
			m.Update(press)
			hint := design.Text(language, "shell.quit_hint")
			if !strings.Contains(m.View().Content, hint) {
				t.Fatal("hint is not localized")
			}
			now = now.Add(2*time.Second - 1)
			m.Update(quitHintExpired{})
			if !strings.Contains(m.View().Content, hint) {
				t.Fatal("early expiry cleared the hint")
			}
			now = now.Add(1)
			m.Update(quitHintExpired{})
			if strings.Contains(m.View().Content, hint) || len(m.content.Notices) != 0 || len(m.content.Blocks) != 0 {
				t.Fatal("expired hint survived or entered history/notices")
			}
			m.Update(press)
			m.Update(quitHintExpired{}) // A stale expiry cannot clear a new window.
			if !strings.Contains(m.View().Content, hint) || m.status.State == Quit {
				t.Fatal("new confirmation was lost to stale expiry")
			}
		})
	}
}

func TestControlCAdmission(t *testing.T) {
	for _, events := range []bool{false, true} {
		for _, composer := range []bool{false, true} {
			m := newShell(Options{Width: 80}, func() time.Time { return time.Unix(100, 0) })
			m.events = events
			if composer {
				m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
				m.status.State = Live
			}
			ignored := []tea.Msg{
				tea.KeyReleaseMsg{Code: 'c', Mod: tea.ModCtrl},
				tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl, IsRepeat: true},
				tea.PasteMsg{Content: "\x03"},
			}
			for _, msg := range ignored {
				m.Update(msg)
				if m.quitHint() != "" || m.status.State == Quit {
					t.Fatal("release, repeat or paste armed confirmation")
				}
			}
			m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			for _, msg := range ignored {
				m.Update(msg)
				if m.status.State == Quit {
					t.Fatal("release, repeat or paste confirmed quit")
				}
			}
			_, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			if cmd == nil || m.status.State != Quit {
				t.Fatal("Ctrl+D did not quit immediately")
			}
		}
	}
}
