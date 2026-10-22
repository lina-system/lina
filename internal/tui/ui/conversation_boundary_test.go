// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationLongSpeakerKeepsReplyWidth(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		for _, options := range []Options{{Profile: TrueColor, ASCII: true}, {Profile: Monochrome}, {Profile: Monochrome, ASCII: true}} {
			for index, name := range []string{strings.Repeat("A", 100), strings.Repeat("한글👩‍💻é", 20)} {
				t.Run(fmt.Sprintf("%d/%d/%t/%d", width, options.Profile, options.ASCII, index), func(t *testing.T) {
					m := conversationShell()
					options.Width = width
					m.styles = NewStyles(options)
					m.Update(tea.WindowSizeMsg{Width: width, Height: 100})
					m.conversation.id = "main:chosen"
					m.content.Agents = []protocol.Agent{{AgentID: "chosen", Name: name}}
					reply := strings.Repeat("가나👩‍💻é", 12)
					m.content.Blocks = []state.Block{{ID: "reply", Topic: "conversation:chosen", Speaker: protocol.ConversationSpeakerAgent, Text: reply, Done: true}}
					m.refreshBlocks()
					lines := strings.Split(ansi.Strip(m.conversationContent(false)), "\n")
					region := m.conversationPlan().Conversation
					first := lines[region.Y]
					end := strings.Index(first, ": ")
					if !strings.HasPrefix(first, "* ") || end < 0 {
						t.Fatalf("speaker mark and word missing: %q", first)
					}
					prefix := first[:end+2]
					cells := (render.Unicode{}).Width(prefix)
					if cells > 20 || cells > width/2 {
						t.Fatalf("speaker consumes %d of %d cells", cells, width)
					}
					text := strings.TrimPrefix(first, prefix)
					indent := strings.Repeat(" ", cells)
					for _, line := range lines[region.Y+1 : region.Y+region.Height] {
						if strings.TrimSpace(line) == "" {
							break
						}
						if !strings.HasPrefix(line, indent) || (render.Unicode{}).Width(line) > width {
							t.Fatalf("invalid continuation: %q", line)
						}
						text += strings.TrimPrefix(line, indent)
					}
					if text != reply {
						t.Fatalf("wide reply changed: %q, want %q", text, reply)
					}
				})
			}
		}
	}
}

func TestConversationPreservesCodeBoundaryRows(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []string
	}{
		{"boundary", "```\n\ncode\n\n```", []string{"● ", "  code", "  "}},
		{"empty-only", "```\n\n\n```", []string{"● ", "  "}},
		{"empty-fence", "```\n```", []string{"● "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := conversationShell()
			m.styles = NewStyles(Options{Profile: TrueColor, Width: 80})
			m.content.Blocks = []state.Block{
				{ID: "code", Topic: "conversation:fixture", Speaker: protocol.ConversationSpeakerAgent, Text: tc.source, Done: true},
				{ID: "next", Topic: "conversation:fixture", Speaker: protocol.ConversationSpeakerPerson, Text: "next", Done: true},
			}
			m.refreshBlocks()
			lines := strings.Split(ansi.Strip(m.conversationContent(false)), "\n")
			start := m.conversationPlan().Conversation.Y
			want := append(append([]string{}, tc.want...), "", "› next")
			if got := lines[start : start+len(want)]; !reflect.DeepEqual(got, want) {
				t.Fatalf("code boundary and turn gap = %q, want %q", got, want)
			}
		})
	}
}

func TestConversationMetadataDoesNotMutateMarkdownCache(t *testing.T) {
	for _, source := range []string{"```\ncode\n\n```", "<pre>\ncode\n\n"} {
		for _, metadata := range []string{"worked", "pager", "both"} {
			t.Run(metadata+"/"+source, func(t *testing.T) {
				m := conversationShell()
				m.styles = NewStyles(Options{Profile: TrueColor, Width: 80})
				m.content.Blocks = []state.Block{{ID: "reply", Topic: "conversation:fixture", Text: source, Done: true, InlineReply: metadata != "worked"}}
				if metadata != "pager" {
					m.content.Worked = map[string]time.Duration{"reply": time.Second}
				}
				m.refreshBlocks()
				stream := m.blocks["reply"].stream
				config := render.Config{Width: m.conversationPlan().Conversation.Width, Theme: m.styles.options.Theme}
				cached := stream.Markdown(config, m.styles.listBullet(), parseMarkdown)
				original := fmt.Sprintf("%#v", cached)
				first := m.conversationContent(false)
				for range 3 {
					if next := m.conversationContent(false); next != first {
						t.Fatalf("unchanged redraw changed the frame:\n%s\nthen:\n%s", ansi.Strip(first), ansi.Strip(next))
					}
				}
				if got := fmt.Sprintf("%#v", stream.Markdown(config, m.styles.listBullet(), parseMarkdown)); got != original {
					t.Errorf("presentation mutated cached Markdown: %s -> %s", original, got)
				}
				if metadata != "worked" && strings.Count(ansi.Strip(first), design.Text(design.English, "reply.open_pager")) != 1 {
					t.Error("pager metadata must appear exactly once")
				}
			})
		}
	}
}
