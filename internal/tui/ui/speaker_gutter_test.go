// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationSpeakerGutterWrapsAtTextColumn(t *testing.T) {
	for _, width := range []int{40, 100} {
		for _, options := range []Options{{Profile: TrueColor}, {Profile: TrueColor, ASCII: true}, {Profile: Monochrome}, {Profile: Monochrome, ASCII: true}} {
			t.Run(fmt.Sprintf("%d/%d/%t", width, options.Profile, options.ASCII), func(t *testing.T) {
				options.Width = width
				m := newShell(options, time.Now)
				m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
				m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
				person := strings.Repeat("\ud55c\uae00", 28)
				reply := "Reply body"
				m.conversation.id = "main:chosen"
				m.content.Agents = []protocol.Agent{{AgentID: "chosen", Name: "Cedar \ud55c\uae00"}}
				m.content.Blocks = []state.Block{
					{ID: "person", Topic: "conversation:fixture", Speaker: protocol.ConversationSpeakerPerson, Text: person, Done: true},
					{ID: "agent", Topic: "conversation:fixture", Speaker: protocol.ConversationSpeakerAgent, Text: reply, Done: true},
				}
				m.refreshBlocks()
				view := m.conversationContent(false)
				if !strings.HasPrefix(ansi.Strip(view), "Cedar \ud55c\uae00\n") {
					t.Fatal("selected agent missing from header")
				}
				if options.Profile == Monochrome && strings.Contains(view, "\x1b") {
					t.Fatal("monochrome emitted escapes")
				}
				lines := strings.Split(ansi.Strip(view), "\n")
				personPrefix, agentPrefix := "› ", "● "
				if options.ASCII || options.Profile == Monochrome {
					personPrefix, agentPrefix = "> You: ", "* Cedar \ud55c\uae00: "
				}
				if !strings.HasPrefix(lines[1], personPrefix) {
					t.Fatalf("first message = %q, want prefix %q", lines[1], personPrefix)
				}
				indent := strings.Repeat(" ", (render.Unicode{}).Width(personPrefix))
				text := strings.TrimPrefix(lines[1], personPrefix)
				y := 2
				for ; y < len(lines) && strings.TrimSpace(lines[y]) != ""; y++ {
					if !strings.HasPrefix(lines[y], indent) {
						t.Fatalf("continuation lacks %d-cell indent: %q", len(indent), lines[y])
					}
					text += strings.TrimPrefix(lines[y], indent)
				}
				if text != person {
					t.Fatalf("wrapped content changed: %q", text)
				}
				if y+1 >= len(lines) || lines[y+1] != agentPrefix+reply {
					t.Fatalf("missing one blank separator and agent prefix: %q", lines)
				}
				for _, line := range lines {
					if (render.Unicode{}).Width(line) > width {
						t.Fatalf("row exceeds viewport: %q", line)
					}
				}
			})
		}
	}
}
