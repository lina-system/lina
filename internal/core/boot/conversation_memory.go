// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

func (r *conversationRuntime) events(ctx context.Context, through string) ([]ledger.Event, error) {
	var ids []string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		query := `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent','forwarded') OR scaffold_kind IN ('interruption','failure','persona_change','run_handoff','background_notice','coding_handoff','work_completion','timer','timer_work','handoff_check','worker_session','worker_completion'))`
		args := []any{r.conversation}
		if through != "" {
			query += ` AND (speaker_kind NOT IN ('person','forwarded') OR rowid <= (SELECT rowid FROM conv_event WHERE id=?))`
			args = append(args, through)
		}
		query += ` ORDER BY rowid DESC LIMIT 200`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var events []ledger.Event
	journal := r.journal()
	for i := len(ids) - 1; i >= 0; i-- {
		event, err := journal.Get(ctx, ids[i])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

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
	found := through == ""
	for i, event := range visible {
		if event.ID == through {
			copy(visible[i:], visible[i+1:])
			visible[len(visible)-1] = event
			found = true
			break
		}
	}
	// Matching text from another event cannot stand in for a removed input.
	if !found {
		return nil, nil, 0, nil, assemble.ErrHistory
	}
	history, err := assemble.History(visible, r.conversation, r.agent)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	if r.channelBinding != nil && !r.channelBinding.Scope.Memory {
		return history, nil, snapshot.Epoch, visible, nil
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
