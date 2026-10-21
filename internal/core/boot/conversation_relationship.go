// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/memory"
)

// Atropos reads the current observer/person view as optional untrusted evidence.
// This runs inside its bounded collector and uses memory's retraction and model
// endpoint scope filters. It cannot turn a missing view into invented intimacy.
func (r *conversationRuntime) relationshipPacket(ctx context.Context, at time.Time) (assemble.ContextPacket, error) {
	hits, err := memory.New(r.db).CurrentRelationship(ctx, memory.RecallScope{Agent: r.agent, Person: r.person, ExternalModel: true}, at.Unix())
	if err != nil {
		return nil, err
	}
	var packet assemble.ContextPacket
	for _, hit := range hits {
		item := relationshipContext(hit.Item, r.agent)
		packet = append(packet, item)
	}
	return packet, nil
}

func relationshipContext(hit memory.Item, agent string) assemble.ContextItem {
	raw, _ := json.Marshal(struct {
		Kind        string       `json:"kind"`
		Observation string       `json:"observation"`
		Basis       memory.Basis `json:"basis"`
	}{"relationship_view", hit.Content, hit.Basis})
	item := assemble.ContextItem{ID: "memory:" + hit.ID, Revision: strconv.FormatInt(hit.Revision, 10), AgentID: agent, Kind: assemble.Memory, Origin: "personal-memory:" + hit.ID, Text: string(raw)}
	for _, ref := range hit.Evidence {
		item.Evidence = append(item.Evidence, assemble.Ref{ID: ref.ID})
	}
	if hit.Supersedes != "" {
		item.Supersedes = []string{"memory:" + hit.Supersedes}
	}
	return item
}
