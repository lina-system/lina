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
func History(events []ledger.Event, conversation, agent string) ([]provider.Item, error) {
	if conversation == "" || agent == "" {
		return nil, ErrHistory
	}
	var items []provider.Item
	for _, e := range events {
		if e.ConversationID != conversation || e.AgentID != agent {
			return nil, ErrHistory
		}
		if e.Origin.Kind == "imported" || e.ReplayOf != "" {
			continue
		}
		m, err := e.Message()
		if err != nil {
			return nil, err
		}
		if e.ScaffoldKind == "prepare_ack" || e.ScaffoldKind == "request_assembly" {
			continue
		}
		if e.ScaffoldKind == "provider_item" {
			if e.Origin.Entry != guard.EntryAgent && e.Origin.Entry != guard.EntryTool {
				return nil, ErrHistory
			}
			raw := []byte(e.Body.DerivedText())
			if !json.Valid(raw) {
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
		items = append(items, message(role, m.Body()))
	}
	return items, nil
}
