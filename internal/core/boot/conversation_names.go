// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
)

func (r *conversationRuntime) namePacket(ctx context.Context, at time.Time) (assemble.ContextPacket, []memory.Item, error) {
	if r.channelBinding != nil && !r.channelBinding.Scope.Memory {
		return nil, nil, nil
	}
	hits, err := memory.New(r.db).Names(ctx, memory.RecallScope{Agent: r.agent, Person: r.person, ExternalModel: true}, at.Unix())
	if err != nil {
		return nil, nil, err
	}
	var packet assemble.ContextPacket
	var names []memory.Item
	for _, hit := range hits {
		packet = append(packet, nameContext(hit.Item, r.agent))
		names = append(names, hit.Item)
	}
	return packet, names, nil
}

func nameContext(hit memory.Item, agent string) assemble.ContextItem {
	raw, _ := json.Marshal(struct {
		Kind    memory.Kind       `json:"kind"`
		Subject memory.Subject    `json:"subject"`
		Name    *memory.NameValue `json:"name"`
		Scope   memory.Scope      `json:"scope"`
	}{memory.Name, hit.Subject, hit.Name, hit.Scope})
	item := assemble.ContextItem{ID: "memory:" + hit.ID, Revision: strconv.FormatInt(hit.Revision, 10), AgentID: agent, Kind: assemble.Commitments, Origin: "name-memory:" + hit.ID, Text: string(raw)}
	for _, ref := range hit.Evidence {
		item.Evidence = append(item.Evidence, assemble.Ref{ID: ref.ID})
	}
	return item
}

// The ledger stages materials first, then this wrapper adds the counts inside
// the same write as turn completion and reply admission.
type namingCanon struct {
	turn.Canon
	person, agent string
	names         []memory.Item
	replies       []ledger.Admission
}

func (c namingCanon) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return c.Canon.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		for _, admission := range c.replies {
			if admission.Entry != guard.EntryAgent || admission.Input.ScaffoldKind == "provider_item" {
				continue
			}
			// Inspect the ledger's committed masked text, never hidden source bytes.
			masked, err := secrets.Mask(admission.Input.Body)
			if err != nil {
				return err
			}
			if err := reply.RecordNamingBreach(ctx, tx, c.person, c.agent, admission.Input.ID, masked.Text(), c.names); err != nil {
				return err
			}
		}
		return nil
	})
}
