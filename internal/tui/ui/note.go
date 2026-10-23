// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/hex"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func noteWord(text string) (string, string) {
	text = strings.TrimSpace(text)
	i := strings.IndexFunc(text, unicode.IsSpace)
	if i < 0 {
		return text, ""
	}
	return text[:i], strings.TrimSpace(text[i:])
}

func (m *shellModel) noteCommand(text string) (bool, tea.Cmd) {
	head, args := noteWord(text)
	if head != "/note" {
		return false, nil
	}
	help := func(code string) (bool, tea.Cmd) {
		text := design.Text(m.styles.options.Language, "note.help")
		if code != "" {
			text = design.Text(m.styles.options.Language, code) + "\n" + text
		}
		m.openPager(text)
		return true, nil
	}
	if !m.hasCapability("running") {
		return help("capability_not_negotiated")
	}
	if args == "" {
		return help("")
	}
	target, body := noteWord(args)
	rows := m.topicBlocks("running")
	request := protocol.FollowUp{}
	for _, row := range rows {
		if row.RunID == target {
			if body == "" || row.RunState == protocol.RunningStateDone || row.RunState == protocol.RunningStateReadyForReview || row.RunState == protocol.RunningStateFailed || row.RunState == protocol.RunningStateStopped {
				return help("")
			}
			request = protocol.FollowUp{RunID: target, Text: body}
			break
		}
	}
	if request.RunID == "" {
		// A missing explicit run reference must not become text for a different run.
		_, hexErr := hex.DecodeString(target)
		if len(target) == 32 && hexErr == nil || strings.HasPrefix(target, "session:") || strings.HasPrefix(target, "task:") {
			return help("")
		}
		if m.conversation.id == "" || m.conversation.agentMissing || m.conversation.agentPending || m.content.Topics["running"].OutOfSync {
			return help("")
		}
		for _, row := range rows {
			if row.RunState != protocol.RunningStateInProgress || row.RunID == "" || strings.HasPrefix(row.RunID, "task:") || strings.HasPrefix(row.RunID, "session:") {
				continue
			}
			if request.RunID != "" {
				return help("")
			}
			request.RunID = row.RunID
		}
		if request.RunID == "" {
			return help("")
		}
		// Running rows have no conversation metadata. Core checks this scope before
		// queuing the note; a refusal never falls back to another run or model input.
		conversation := m.conversation.id
		request.Text, request.ConversationID = args, &conversation
	}
	generation := m.generation
	return true, func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}
