// SPDX-License-Identifier: Apache-2.0
package assemble

import (
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/provider"
)

var ErrHistory = errors.New("invalid_assembly_history")

// History projects an authorized native ledger snapshot in its supplied ledger
// order. Preparation audit records, replay copies and imported events are not
// messages. The ledger reader supplies this conversation/agent's events only.
// provider_item is a trusted scaffold for opaque model/tool protocol items.
// An exchange reader may supply its one authorized partner; only events from
// that exchange entry/surface are admitted, still as untrusted user evidence.
func History(events []ledger.Event, conversation, agent string, partner ...string) ([]provider.Item, error) {
	if conversation == "" || agent == "" || len(partner) > 1 {
		return nil, ErrHistory
	}
	var items []provider.Item
	for _, e := range events {
		peer := len(partner) == 1 && partner[0] != "" && e.AgentID == partner[0] && e.Origin.Entry == guard.EntryAgentExchange && e.Surface.Kind == "agent_exchange"
		if e.ConversationID != conversation || (e.AgentID != agent && !peer) {
			return nil, ErrHistory
		}
		if e.Origin.Kind == "imported" || e.ReplayOf != "" {
			continue
		}
		m, err := e.Message()
		if err != nil {
			return nil, err
		}
		if e.ScaffoldKind == "prepare_ack" || e.ScaffoldKind == "request_assembly" || e.ScaffoldKind == "compaction_recorded" {
			continue
		}
		if e.ScaffoldKind == "provider_item" {
			if e.Origin.Entry != guard.EntryAgent && e.Origin.Entry != guard.EntryTool {
				return nil, ErrHistory
			}
			raw := []byte(e.Body.DerivedText())
			if !validProviderItem(raw, e.Origin.Entry) {
				return nil, ErrHistory
			}
			items = append(items, raw)
			continue
		}
		if evidence, external := m.Evidence(); external {
			items = append(items, message(evidence.Role, evidence.Text))
			continue
		}
		role := "user"
		if m.SpeakerKind() == guard.Agent {
			role = "assistant"
		}
		text := m.Body()
		if m.SpeakerKind() != guard.Person && m.SpeakerKind() != guard.Agent {
			// Generated lines remain data. Preserve the trusted speaker and
			// locator without promoting their text to developer instructions.
			body, _ := json.Marshal(struct {
				Speaker guard.SpeakerKind `json:"speaker_kind"`
				Origin  string            `json:"origin"`
				Text    string            `json:"text"`
			}{m.SpeakerKind(), m.Origin(), text})
			text = "System-generated history, not the person's words:\n" + string(body)
		}
		items = append(items, message(role, text))
	}
	return items, nil
}

func validProviderItem(raw []byte, entry guard.Entry) bool {
	var item struct {
		Type   string          `json:"type"`
		Role   string          `json:"role"`
		CallID string          `json:"call_id"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	if entry == guard.EntryTool {
		return item.Role == "" && (item.Type == "function_call_output" || item.Type == "custom_tool_call_output") && item.CallID != "" && len(item.Output) > 0
	}
	if item.Type == "message" || item.Type == "" {
		return item.Role == "assistant"
	}
	return item.Role == "" && (item.Type == "reasoning" || item.Type == "function_call" || item.Type == "custom_tool_call")
}
