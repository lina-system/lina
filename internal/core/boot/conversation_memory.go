// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

// modelContext filters both ledger evidence and memory through the same scoped
// removal view. The person's original ledger remains available to the UI.
func (r *conversationRuntime) modelContext(ctx context.Context, through string) ([]provider.Item, []turn.Reference, uint64, []ledger.Event, error) {
	snapshot, err := r.removals.Snapshot(ctx, r.person, r.agent)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	events, err := r.events(ctx, through)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	visible := make([]ledger.Event, 0, len(events))
	for _, event := range events {
		if snapshot.Visible(retract.Ref{Kind: retract.Event, ID: event.ID}) {
			visible = append(visible, event)
		}
	}
	// A queued input may precede the prior turn's later reply in the ledger.
	// Put that exact event in the assembler's current-input position, preserving
	// every other visible event and the ordering recorded for replay evidence.
	for i, event := range visible {
		if event.ID == through {
			copy(visible[i:], visible[i+1:])
			visible[len(visible)-1] = event
			break
		}
	}
	history, err := assemble.History(visible, r.conversation, r.agent)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	profile, err := memory.New(r.db).Profile(ctx, memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}, time.Now().Unix(), 32, 4096, func(text string) int { return len(text) })
	if err != nil {
		return nil, nil, 0, nil, err
	}
	refs := make([]turn.Reference, 0, len(profile))
	items := make([]provider.Item, 0, len(profile)+len(history))
	for _, hit := range profile {
		if !snapshot.Visible(retract.Ref{Kind: retract.Memory, ID: hit.ID}) {
			continue
		}
		refs = append(refs, turn.Reference{ID: hit.ID, Revision: strconv.FormatInt(hit.Revision, 10)})
		raw, _ := json.Marshal(map[string]string{"role": "user", "content": "Remembered statement (" + hit.ID + "): " + hit.Content})
		items = append(items, provider.Item(raw))
	}
	return append(items, history...), refs, snapshot.Epoch, visible, nil
}

func (r *conversationRuntime) validateModelContext(ctx context.Context, epoch uint64, refs []turn.Reference) error {
	if err := r.removals.Validate(ctx, epoch); err != nil {
		return err
	}
	scope := memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}
	for _, ref := range refs {
		hits, err := memory.New(r.db).Recall(ctx, scope, memory.Query{ID: ref.ID, At: time.Now().Unix(), Limit: 1})
		if err != nil {
			return err
		}
		if len(hits) != 1 || strconv.FormatInt(hits[0].Revision, 10) != ref.Revision {
			return retract.ErrStaleSnapshot
		}
	}
	// A correction or removal racing any of the scoped reads cannot validate an
	// earlier epoch. Corrections are caught by each original item's visibility.
	if err := r.removals.Validate(ctx, epoch); err != nil {
		return err
	}
	if _, err := r.agents.Get(ctx, r.agent); err != nil {
		return err
	}
	return nil
}

// topicAuthorized runs at subscription admission and before each delivery.
func (r *conversationRuntime) topicAuthorized(ctx context.Context, topic string) bool {
	for _, prefix := range []string{"conversation:main:", "results:main:"} {
		if len(topic) > len(prefix) && topic[:len(prefix)] == prefix {
			_, err := r.agents.Get(ctx, topic[len(prefix):])
			return err == nil
		}
	}
	return topic == "core"
}
