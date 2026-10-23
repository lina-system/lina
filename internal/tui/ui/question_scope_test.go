// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationQuestionScope(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, outcome := range []protocol.DecisionOutcome{"", protocol.DecisionOutcomeResolved} {
			name := "live/" + string(outcome)
			if replay {
				name = "snapshot/" + string(outcome)
			}
			t.Run(name, func(t *testing.T) {
				m := conversationShell()
				m.size = layout.Size{Width: 100, Height: 30}
				m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "waiting", Version: "1.0"})
				q := protocol.Question{ConversationID: "main:fixture", Card: &protocol.DecisionCard{ID: "question", Revision: "1", Agent: "Same name", Summary: "CAPSULE-QUESTION", Targets: []protocol.DecisionTarget{}}}
				if outcome != "" {
					q.Card.Outcome = &outcome
				}
				cursor := protocol.StreamCursor{Epoch: "1", Seq: "1"}
				var payload protocol.Payload
				if replay {
					raw, err := json.Marshal(q)
					if err != nil {
						t.Fatal(err)
					}
					payload = protocol.Snapshot{Topic: "waiting", Cursor: cursor, RevocationEpoch: "1", Items: []protocol.StreamItem{{Key: "question", Kind: protocol.KindQuestion, Revision: "1", Payload: raw}}}
				} else {
					m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "waiting"}})
					q.Stream = &protocol.StreamMetadata{Topic: "waiting", Key: "question", Revision: "1", Cursor: cursor}
					payload = q
				}
				projection, err := projectStream(protocol.Envelope{Epoch: "1", Kind: protocol.KindQuestion, Payload: payload})
				if err != nil {
					t.Fatal(err)
				}
				m.Update(shellData{Generation: 1, Message: projection.message})
				// Membership follows the conversation id, never the agent's display name.
				for _, conversation := range []string{"main:fixture", "other", "", "main:fixture"} {
					m.conversation.id = conversation
					plan := m.conversationPlan()
					rows := strings.Split(m.conversationContent(false), "\n")
					transcript := strings.Join(rows[plan.Conversation.Y:plan.Conversation.Y+plan.Conversation.Height], "\n")
					want := conversation == q.ConversationID
					if strings.Contains(transcript, q.Card.Summary) != want {
						t.Errorf("conversation=%q question visible=%v, want %v", conversation, !want, want)
					}
					m.conversation.pager.transcript = true
					if strings.Contains(m.pagerText(), q.Card.Summary) != want {
						t.Errorf("conversation=%q pager question visible=%v, want %v", conversation, !want, want)
					}
					m.conversation.pager.transcript = false
				}
				m.conversation.id = "other"
				if outcome == "" {
					if !strings.Contains(m.topicText("waiting"), q.Card.Summary) {
						t.Error("foreign pending question disappeared from global waiting")
					}
					m.cardKey(term.Key{Name: "f6", Events: true})
					if card, ok := m.content.CurrentCard(); !ok || card.ID != q.Card.ID {
						t.Error("foreign pending question cannot be opened")
					}
				} else if len(m.topicBlocks("waiting")) != 0 {
					t.Error("resolved question remains in global waiting")
				}
			})
		}
	}
}
