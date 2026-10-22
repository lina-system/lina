// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type conversationAnchor struct {
	block string
	row   int
}

type conversationViewport struct {
	anchor         conversationAnchor
	changed        map[string]bool
	unseen         bool
	wheelAt        time.Time
	wheelDirection tea.MouseButton
	wheelRows      int
}

func transcriptBlock(b state.Block) bool {
	return b.Topic != "core" && b.Topic != "running" && (b.Topic != "waiting" || b.Card != nil) && b.Topic != "progress" && b.Kind != protocol.KindTurnState
}

func (m *shellModel) scrollConversation(rows int) {
	m.conversation.scroll = max(0, m.conversation.scroll+rows)
	m.conversation.viewport.anchor = conversationAnchor{}
	if m.conversation.scroll == 0 {
		m.conversation.viewport.unseen = false
	}
}

func (m *shellModel) conversationWheel(msg tea.MouseWheelMsg) {
	if !m.hasComposer() || m.conversation.pager.open || m.accounts.open || m.channels.open || m.talk.open || m.content.CardView.ID != "" {
		return
	}
	region := m.conversationPlan().Conversation
	if msg.X < region.X || msg.X >= region.X+region.Width || msg.Y < region.Y || msg.Y >= region.Y+region.Height {
		return
	}
	if msg.Button != tea.MouseWheelUp && msg.Button != tea.MouseWheelDown {
		return
	}
	v := &m.conversation.viewport
	now := m.now()
	elapsed := now.Sub(v.wheelAt)
	if msg.Button == v.wheelDirection && !v.wheelAt.IsZero() && elapsed >= 0 && elapsed <= 120*time.Millisecond {
		v.wheelRows = min(12, v.wheelRows+3)
	} else {
		v.wheelRows = 3
	}
	v.wheelAt, v.wheelDirection = now, msg.Button
	rows := v.wheelRows
	if msg.Button == tea.MouseWheelDown {
		rows = -rows
	}
	m.scrollConversation(rows)
}

// Reconcile the retained first visible block/row before painting. Appending or
// replacing output below it must not change the viewport's start. A manual
// scroll clears the anchor so its requested offset becomes the next anchor.
func (m *shellModel) conversationStart(anchors []conversationAnchor, total, height int) int {
	c := &m.conversation
	v := &c.viewport
	bottom := max(0, total-height)
	c.scroll = min(c.scroll, bottom)
	start := bottom - c.scroll
	if c.scroll > 0 && v.anchor.block != "" {
		for i, anchor := range anchors {
			if anchor == v.anchor {
				start = min(i, bottom)
				c.scroll = bottom - start
				break
			}
		}
	}
	if c.scroll == 0 {
		v.anchor = conversationAnchor{}
		v.unseen = false
	} else {
		if start < len(anchors) {
			v.anchor = anchors[start]
		}
		for i := start + height; i < len(anchors); i++ {
			if v.changed[anchors[i].block] {
				v.unseen = true
				break
			}
		}
	}
	clear(v.changed)
	return start
}

func (m *shellModel) conversationStatus(status string, overflow bool) string {
	if m.conversation.viewport.unseen {
		marker := "  " + design.Text(m.styles.options.Language, "conversation.new_messages")
		status = ansi.Truncate(status, max(0, m.size.Width-ansi.StringWidth(marker)), "") + marker
	}
	status += "  /help  Enter  Ctrl+D  /memory  /agents"
	if overflow {
		hint := "  " + design.Text(m.styles.options.Language, "conversation.scroll_hint")
		// Keep the new-output marker and existing status ahead of the optional hint.
		if ansi.StringWidth(status+hint) <= m.size.Width {
			status += hint
		}
	}
	return status
}
