// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
)

// This is the first interaction of T-16 E6: opening the selected work.
// A card with an unknown plan must still open; the client must not invent stages.
func TestWorkPanelOpenStageList(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{120, 100} {
				t.Run(fmt.Sprintf("%s/ascii=%t/%dx34", language, ascii, width), func(t *testing.T) {
					cards := navigationCards(2)
					m := navigationShell(width, cards)
					m.styles = NewStyles(Options{Profile: TrueColor, Language: language, ASCII: ascii})
					m.View()
					m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					back := "‹"
					if ascii {
						back = "<"
					}
					view := ansi.Strip(m.View().Content)
					if !strings.Contains(view, back+" "+cards[1].Name) {
						t.Fatalf("Enter did not open the selected work's stage list:\n%s", view)
					}
					if strings.Contains(view, cards[0].Name) {
						t.Fatal("opened work still shows another card")
					}
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					assertSelectedWorkVisible(t, m, cards[1])
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
					if m.conversation.workFocused || m.content.Draft.Text != "keep 초안" {
						t.Fatal("returning to the composer lost the draft or panel focus")
					}
				})
			}
		}
	}
}
