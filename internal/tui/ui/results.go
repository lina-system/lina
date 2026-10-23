// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (m *shellModel) topicBlocks(topic string) []state.Block {
	var blocks []state.Block
	for _, b := range m.content.Blocks {
		if b.Topic == topic {
			if topic == "running" && b.RunState != "" {
				b.Text = m.runningRowText(b)
			}
			if b.Card != nil {
				card := *b.Card
				for _, current := range m.content.Cards {
					if current.ID == card.ID {
						card = current
						break
					}
				}
				if card.Outcome != "" {
					continue
				}
				b.Text = m.cardSummary(card)
			}
			blocks = append(blocks, b)
		}
	}
	return blocks
}
func (m *shellModel) topicText(topic string) string {
	if !m.hasCapability(topic) {
		return design.Text(m.styles.options.Language, "capability_not_negotiated")
	}
	var lines []string
	for _, b := range m.topicBlocks(topic) {
		lines = append(lines, b.Text)
	}
	if topic == "waiting" && len(lines) == 0 {
		return design.Text(m.styles.options.Language, "waiting.empty")
	}
	return strings.Join(lines, "\n")
}
func (m *shellModel) resultCards() []state.Block {
	var cards []state.Block
	for _, b := range m.content.Blocks {
		if b.Kind == protocol.KindResult {
			cards = append(cards, b)
		}
	}
	return cards
}
func (m *shellModel) selectedCard() (state.Block, bool) {
	cards := m.resultCards()
	if len(cards) > 0 {
		index := m.conversation.selected
		if index < 0 {
			index = len(cards) - 1
		}
		index = min(index, len(cards)-1)
		return cards[index], true
	}
	return state.Block{}, false
}
func resultText(b state.Block) string {
	if b.Body != "" {
		return b.Body
	}
	return b.Text
}

func (m *shellModel) cardText() string {
	if b, ok := m.selectedCard(); ok {
		return resultText(b)
	}
	var lines []string
	for _, b := range m.content.Blocks {
		if strings.HasPrefix(b.Topic, "conversation:") || b.Local {
			lines = append(lines, b.Text)
		}
	}
	return strings.Join(lines, "\n\n")
}

func (m *shellModel) conversationBlock(b state.Block) bool {
	// Waiting is global, but a question remains part of its originating
	// conversation even after resolution or replay to a new client.
	if b.Card != nil && b.Card.Kind == protocol.KindQuestion {
		return m.conversation.id != "" && b.Card.ConversationID == m.conversation.id
	}
	return b.Topic != "core" && b.Topic != "running" && (b.Topic != "waiting" || b.Card != nil) && b.Topic != "progress" && b.Kind != protocol.KindTurnState && b.Kind != protocol.KindToolActivity
}

func (m *shellModel) conversationName() string {
	if m.conversation.agentMissing {
		return design.Text(m.styles.options.Language, "agents.unavailable")
	}
	for _, a := range m.content.Agents {
		if a.AgentID == m.activeAgent() {
			return sanitize.Display(a.Name)
		}
	}
	return "LINA"
}

func (m *shellModel) speakerName(b state.Block) string {
	if b.Local || b.Speaker == protocol.ConversationSpeakerPerson {
		return design.Text(m.styles.options.Language, "person")
	}
	if b.Speaker == protocol.ConversationSpeakerAgent || b.Kind == protocol.KindOutputDelta {
		return m.conversationName()
	}
	return ""
}
