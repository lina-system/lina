// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (m *shellModel) conversationPlan() layout.Layout {
	m.syncComposer()
	activityRows := 0
	if m.activityRequested() {
		activityRows = len(strings.Split(m.activityText(), "\n"))
	}
	cardRows := len(m.topicBlocks("waiting"))
	cardCount := cardRows
	if summary := m.homeWaiting(); summary != nil {
		cardRows = len(summary.Lines)
		cardCount = cardRows + summary.More
	}
	return layout.Plan(m.size, layout.Caps{SidePane: true, Running: m.hasCapability("running"), Cards: m.hasCapability("waiting")}, layout.Shape{WorkPanel: m.hasCapability("progress"), ActivityRows: activityRows, SidePane: m.conversation.pager.open, ComposerRows: m.conversation.composer.Height(), RunningCount: len(m.topicBlocks("running")), CardCount: cardCount, CardRows: cardRows, Attachments: len(m.content.Draft.Attachments) > 0})
}

func (m *shellModel) conversationContent(showNotice bool) string {
	plan := m.conversationPlan()
	rows := make([]string, m.size.Height)
	clip := func(s string) string { return ansi.Truncate(s, m.size.Width, "") }
	rows[plan.Status.Y] = m.conversationFooter(showNotice)
	if plan.Tier == layout.TooSmall {
		if plan.Notice.Height > 0 {
			rows[0] = clip(design.Text(m.styles.options.Language, "shell.too_small"))
		}
		return strings.Join(rows, "\n")
	}
	if m.conversation.workFocused && plan.SidePane.Height == 0 {
		for i, line := range m.workPanelRows(plan.Conversation.Width, plan.Conversation.Height) {
			rows[plan.Conversation.Y+i] = line
		}
		rows[plan.Header.Y] = clip(m.styles.Title.Render(m.conversationName()))
		return strings.Join(rows, "\n")
	}
	if m.conversation.pager.open && plan.SidePane.Height == 0 {
		p := &m.conversation.pager
		lines := m.pagerRows()
		p.offset = min(p.offset, max(0, len(lines)-(m.size.Height-1)))
		for y := 0; y < m.size.Height-1 && p.offset+y < len(lines); y++ {
			rows[y] = clip(m.styles.markdownRow(lines[p.offset+y]))
		}
		return strings.Join(rows, "\n")
	}
	name := m.conversationName()
	rows[plan.Header.Y] = clip(m.styles.Title.Render(name))
	type transcriptRow struct {
		body   render.StyledRow
		prefix string
		person bool
		muted  bool
	}
	var transcript []transcriptRow
	var anchors []conversationAnchor
	plainRow := func(text string) render.StyledRow { return render.StyledRow{Spans: []render.Span{{Text: text}}} }
	if !m.hasCapability("conversation") {
		code := "account.empty"
		if !m.accountsKnown {
			code = "account.loading"
			if m.accounts.code != "" {
				code = m.accounts.code
			}
		}
		for _, row := range m.accountRows {
			if row.Status == "ok" {
				code = "account.ready"
				break
			}
		}
		transcript = append(transcript, transcriptRow{body: plainRow(design.Text(m.styles.options.Language, code) + "  /accounts")})
	}
	groups := m.toolGroups()
	appendTools := func(anchor string) {
		for _, group := range groups {
			if group.calls[0].Anchor != anchor {
				continue
			}
			for _, row := range m.toolGroupRows(group, plan.Conversation.Width) {
				transcript = append(transcript, transcriptRow{body: plainRow(row), muted: true})
			}
		}
	}
	appendTools("")
	hasItems := false
	for _, b := range m.content.Blocks {
		if !m.conversationBlock(b) {
			continue
		}
		hasItems = true
		cached := m.blocks[b.ID]
		if cached.stream == nil {
			continue
		}
		person := b.Local || b.Speaker == protocol.ConversationSpeakerPerson
		prefix := ""
		if person || b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindOutputDelta {
			prefix = m.styles.speakerPrefix(person, name, plan.Conversation.Width)
		}
		indent := strings.Repeat(" ", (render.Unicode{}).Width(prefix))
		stream := cached.stream
		if b.InlineReply && m.resultStreams[b.ID].stream != nil {
			stream = m.resultStreams[b.ID].stream
		}
		body := stream.Markdown(render.Config{Width: plan.Conversation.Width - len(indent), Theme: m.styles.options.Theme}, m.styles.listBullet(), parseMarkdown)
		// Paragraph spacing belongs inside a message; boundaries get exactly one
		// section gap even if the provider ended a message with empty lines.
		blank := func(row render.StyledRow) bool {
			for _, span := range row.Spans {
				if span.Style.Code || strings.TrimSpace(span.Text) != "" {
					return false
				}
			}
			return true
		}
		for len(body) > 0 && blank(body[0]) {
			body = body[1:]
		}
		for len(body) > 0 && blank(body[len(body)-1]) {
			body = body[:len(body)-1]
		}
		// Markdown owns these rows. Appending presentation metadata to a
		// shortened slice must not overwrite the cache's trimmed boundary rows.
		body = slices.Clone(body)
		if b.Kind == protocol.KindResult && !b.InlineReply {
			if b.CompletionLabel != "" {
				body = append([]render.StyledRow{plainRow(sanitize.Display(b.CompletionLabel))}, body...)
			}
			body = append([]render.StyledRow{plainRow(design.Text(m.styles.options.Language, "result") + "  " + sanitize.Display(b.Title) + "  Ctrl+O")}, body...)
		}
		toolRowsAt := len(body)
		workedRow := -1
		if elapsed, ok := m.content.Worked[b.ID]; ok {
			workedRow = len(body)
			body = append(body, plainRow(fmt.Sprintf(design.Text(m.styles.options.Language, "activity.worked_for"), formatElapsed(elapsed))))
		}
		if b.InlineReply {
			body = append(body, plainRow(design.Text(m.styles.options.Language, "reply.open_pager")))
		}
		if len(body) == 0 && !slices.ContainsFunc(groups, func(group toolGroup) bool { return group.calls[0].Anchor == b.ID }) {
			continue
		}
		// The account placeholder has no retained conversation identity.
		for len(anchors) < len(transcript) {
			anchors = append(anchors, conversationAnchor{})
		}
		if len(transcript) > 0 {
			transcript = append(transcript, make([]transcriptRow, m.styles.tokens.SpaceSection.Cells)...)
			// Anchor separator rows to the following block without shifting
			// its body row identities when older messages are inserted above it.
			for row := -m.styles.tokens.SpaceSection.Cells; row < 0; row++ {
				anchors = append(anchors, conversationAnchor{block: b.ID, row: row})
			}
		}
		blockStart := len(transcript)
		if toolRowsAt == 0 {
			appendTools(b.ID)
		}
		for i, row := range body {
			lead := indent
			if i == 0 {
				lead = prefix
			}
			if i == workedRow {
				lead = ""
			}
			transcript = append(transcript, transcriptRow{body: row, prefix: lead, person: person, muted: i == workedRow})
			if i+1 == toolRowsAt {
				appendTools(b.ID)
			}
		}
		for row := 0; row < len(transcript)-blockStart; row++ {
			anchors = append(anchors, conversationAnchor{block: b.ID, row: row})
		}
	}
	// Starter hints are local presentation, never reducer blocks or Core items.
	// Wait for history so reconnecting to an existing conversation cannot flash
	// a first-run screen. Account setup can show hints before chat is negotiated.
	_, historyKnown := m.content.Topics["conversation:"+m.conversation.id]
	if !hasItems && !m.conversation.agentMissing && !m.conversation.agentPending && (historyKnown || !m.hasCapability("conversation")) {
		status := m.styles.presentation(m.status)
		rows[plan.Header.Y] = clip(m.styles.wordmark() + "  " + m.styles.Title.Render(name) + "  " + m.styles.Text.Render(status.Label))
		var hints []transcriptRow
		for _, id := range []string{"conversation.start.memory", "conversation.start.voice", "conversation.start.task"} {
			hints = append(hints, transcriptRow{body: plainRow(design.Text(m.styles.options.Language, id)), muted: true})
		}
		transcript = append(hints, transcript...)
	}
	start := m.conversationStart(anchors, len(transcript), plan.Conversation.Height)
	m.conversation.viewport.overflow = len(transcript) > plan.Conversation.Height
	rows[plan.Status.Y] = m.conversationFooter(showNotice)
	for y, line := range transcript[start:] {
		if y >= plan.Conversation.Height {
			break
		}
		styles := m.styles
		if line.person {
			styles.Text = styles.Muted
		}
		prefix := ""
		if line.prefix != "" {
			prefix = m.styles.Accent.Render(line.prefix)
		}
		rows[plan.Conversation.Y+y] = clip(prefix + styles.markdownRow(line.body))
		if line.muted {
			width := plan.Conversation.Width - (render.Unicode{}).Width(line.prefix)
			rows[plan.Conversation.Y+y] = clip(prefix + m.styles.Muted.Render(render.Truncate(line.body.Spans[0].Text, width, render.Unicode{})))
		}
	}
	drawTopic := func(topic string, region layout.Rect) {
		if region.Height == 0 {
			return
		}
		if topic == "waiting" {
			if summary := m.homeWaiting(); summary != nil {
				total := len(summary.Lines) + summary.More
				label := design.Text(m.styles.options.Language, "waiting") + " " + strconv.Itoa(total)
				shown := min(region.Height, len(summary.Lines))
				for i := 0; i < max(1, shown); i++ {
					line := label
					if i < shown {
						line += "  " + summary.Lines[i]
					}
					if i == max(1, shown)-1 && total > shown {
						suffix := "  " + fmt.Sprintf(design.Text(m.styles.options.Language, "progress.more"), strconv.Itoa(total-shown))
						line = render.Truncate(line, max(0, region.Width-(render.Unicode{}).Width(suffix)), render.Unicode{}) + suffix
					}
					rows[region.Y+i] = clip(line)
				}
				return
			}
		}
		blocks := m.topicBlocks(topic)
		label := design.Text(m.styles.options.Language, topic) + " " + strconv.Itoa(len(blocks))
		if topic == "waiting" && len(m.content.Cards) > 0 {
			label += "  F6"
		}
		for i := 0; i < region.Height && i < len(blocks); i++ {
			rows[region.Y+i] = clip(label + "  " + blocks[i].Text)
		}
		if len(blocks) > region.Height {
			rows[region.Y+region.Height-1] = clip(label + "  +" + strconv.Itoa(len(blocks)-region.Height+1))
		}
	}
	if plan.CombinedWork {
		rows[plan.Running.Y] = fmt.Sprintf("%s %d  %s %d", design.Text(m.styles.options.Language, "running"), plan.RunningCount, design.Text(m.styles.options.Language, "waiting"), plan.CardCount)
	} else {
		drawTopic("running", plan.Running)
		drawTopic("waiting", plan.Cards)
	}
	if plan.Attachments.Height > 0 {
		rows[plan.Attachments.Y] = clip(strings.Join(m.content.Draft.Attachments, "  "))
	}
	if plan.Activity.Height > 0 {
		for i, line := range strings.Split(m.activityText(), "\n") {
			rows[plan.Activity.Y+i] = m.styles.Muted.Render(line)
		}
	}
	if plan.ComposerRule.Height > 0 {
		rule := m.styles.tokens.GlyphUnicodeRule
		if m.styles.options.ASCII {
			rule = m.styles.tokens.GlyphAsciiRule
		}
		rows[plan.ComposerRule.Y] = m.styles.Line.Render(strings.Repeat(rule, plan.ComposerRule.Width))
	}
	m.conversation.composer.SetHeight(plan.Composer.Height)
	for i, line := range strings.Split(m.conversation.composer.View(), "\n") {
		if i >= plan.Composer.Height {
			break
		}
		// Keep the prompt on the first visible row even when the draft scrolls.
		prefix := strings.Repeat(" ", m.styles.composerPromptWidth())
		if i == 0 {
			prefix = m.styles.Accent.Render(m.styles.composerPrompt())
		}
		rows[plan.Composer.Y+i] = clip(prefix + line)
	}
	if plan.SidePane.Height > 0 && !m.conversation.pager.open && m.hasCapability("progress") {
		lines := m.workPanelRows(plan.SidePane.Width, plan.SidePane.Height)
		for y := 0; y < plan.SidePane.Height; y++ {
			left := rows[plan.SidePane.Y+y]
			// The panel and terminal use codepoint cell widths. Clip at that
			// boundary, then retain ANSI styling with the library's grapheme budget.
			visible := render.Truncate(ansi.Strip(left), plan.Conversation.Width, render.Unicode{})
			left = ansi.Truncate(left, ansi.StringWidth(visible), "")
			left += strings.Repeat(" ", max(0, plan.SidePane.X-(render.Unicode{}).Width(ansi.Strip(left))))
			right := ""
			if y < len(lines) {
				right = lines[y]
			}
			rows[plan.SidePane.Y+y] = left + right
		}
	}
	if plan.SidePane.Height > 0 && m.conversation.pager.open {
		p := &m.conversation.pager
		lines := m.pagerRows()
		p.offset = min(p.offset, max(0, len(lines)-plan.SidePane.Height))
		lines = lines[p.offset:]
		for y := 0; y < plan.SidePane.Height; y++ {
			left := ansi.Truncate(rows[plan.SidePane.Y+y], plan.Conversation.Width, "")
			left += strings.Repeat(" ", max(0, plan.SidePane.X-ansi.StringWidth(left)))
			right := ""
			if y < len(lines) {
				right = m.styles.markdownRow(lines[y])
			}
			rows[plan.SidePane.Y+y] = left + right
		}
	}
	return strings.Join(rows, "\n")
}
