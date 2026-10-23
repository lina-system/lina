// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// This whitelist is intentionally narrower than ContextPacket: personal memory
// and all other context sources must remain reconstructible from their stores.
func personaStateItem(item assemble.ContextItem, agent, conversation string) bool {
	if item.AgentID != agent {
		return false
	}
	switch item.ID {
	case "persona:" + agent + ":revision", "persona-expression:" + agent, "persona-settings:" + agent, "persona-methodology:" + agent:
		return item.Kind == assemble.Commitments
	case "persona-emotion:" + agent + ":" + conversation:
		return item.Kind == assemble.Clock
	}
	return false
}
func pinPersonaState(packet assemble.ContextPacket, agent, conversation string) error {
	for i := range packet {
		item := &packet[i]
		if !personaStateItem(*item, agent, conversation) {
			continue
		}
		body, err := secrets.Mask(item.Text)
		if err != nil {
			return err
		}
		item.Text = body.Text()
		hash := sha256.Sum256([]byte(item.Text))
		item.Revision = hex.EncodeToString(hash[:])
	}
	return nil
}

func (r *conversationRuntime) recordContextSources(ctx context.Context, packet assemble.ContextPacket, report assemble.Report) ([]ledger.ContextSource, error) {
	var out []ledger.ContextSource
	for _, ref := range report.Items {
		var item assemble.ContextItem
		for _, candidate := range packet {
			if candidate.ID == ref.ID && candidate.Revision == ref.Revision {
				item = candidate
				break
			}
		}
		source := ledger.ContextSource{Reference: ledger.Reference{ID: ref.ID, Revision: ref.Revision}}
		switch {
		case personaStateItem(item, r.agent, r.conversation):
			source.Source = "persona-state-v1"
			body, err := secrets.Mask(item.Text)
			if err != nil {
				return nil, err
			}
			prepared, err := r.materials.Prepare(ctx, body)
			if err != nil {
				return nil, err
			}
			var material ledger.Material
			err = r.db.Write(ctx, func(tx *canon.Tx) error { var err error; material, err = prepared.Insert(ctx, tx); return err })
			err = errors.Join(err, prepared.Close())
			if err != nil {
				return nil, err
			}
			source.Material = &material
			for _, e := range item.Evidence {
				source.Evidence = append(source.Evidence, ledger.Reference{ID: e.ID, Revision: e.Revision})
			}
		case item.Kind == assemble.Clock && strings.HasPrefix(item.ID, "clock:"):
			source.Source = "clock-v1"
		case item.Kind == assemble.SkillBody:
			source.Source = "skill-v1"
		case item.Kind == assemble.Commitments && strings.HasPrefix(item.Origin, "name-memory:"):
			source.Source = "name-v1"
		case item.Kind == assemble.Memory && item.Origin != "":
			source.Source = "relationship-v1"
		default:
			return nil, errors.New("unsupported conversation context source")
		}
		out = append(out, source)
	}
	return out, nil
}
