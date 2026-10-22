// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/assemble"
)

func (r *conversationRuntime) methodologyContext(ctx context.Context) (assemble.ContextPacket, error) {
	m, err := r.agents.Methodology(ctx, r.agent)
	if err != nil || m.ID == "" {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return assemble.ContextPacket{{ID: "persona-methodology:" + r.agent, AgentID: r.agent, Kind: assemble.Commitments, Text: string(raw)}}, nil
}
