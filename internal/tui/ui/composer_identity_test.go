// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestComposerIdentityFollowsClientFocus(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, flags := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/keyboard-%d", language, flags), func(t *testing.T) {
				m := conversationShell()
				m.styles = NewStyles(Options{Width: 120, Profile: Monochrome, Language: language})
				m.content.Agents = []protocol.Agent{{AgentID: "default", Name: "LINA"}, {AgentID: "chosen", Name: "QA-Beta"}}
				m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
				m.Update(tea.KeyboardEnhancementsMsg{Flags: flags})
				assertIdentity := func(name string) {
					t.Helper()
					rows := strings.Split(ansi.Strip(m.View().Content), "\n")
					composer := rows[m.conversationPlan().Composer.Y]
					if !strings.Contains(rows[0], name) || !strings.Contains(composer, name) {
						t.Errorf("header and composer must name %q: header=%q composer=%q", name, rows[0], composer)
					}
					if name != "LINA" && (strings.Contains(composer, "LINA") || strings.Contains(composer, "리나")) {
						t.Errorf("composer retained default identity: %q", composer)
					}
					hint := "Alt+Enter"
					if flags != 0 {
						hint = "Shift+Enter"
					}
					if !strings.Contains(composer, hint) {
						t.Errorf("composer lost keyboard hint %q: %q", hint, composer)
					}
					if m.content.Draft.Text != "" || m.conversation.composer.Value() != "" {
						t.Fatal("placeholder became draft text")
					}
				}
				m.conversation.id = "main:chosen"
				assertIdentity("QA-Beta")
				// Names are display data, including percent signs and Korean text.
				m.content.Agents[1].Name = "별 %s"
				assertIdentity("별 %s")
				m.Update(shellBegin{Generation: 2})
				m.reconnectHistory(m.content.Agents)
				m.agentDiscoveryDone()
				m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
				assertIdentity("별 %s")
				m.conversation.id = "main:default"
				assertIdentity("LINA")
			})
		}
	}
}
