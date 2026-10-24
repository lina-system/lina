// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

var conversationModelContextSources = `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent','forwarded') OR scaffold_kind IN ` + scaffoldSQLList(modelContextScaffolds) + `)`

func (r *conversationRuntime) events(ctx context.Context, through string) ([]ledger.Event, error) {
	var ids []string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		query := conversationModelContextSources
		args := []any{r.conversation}
		if through != "" {
			query += ` AND (speaker_kind NOT IN ('person','forwarded') OR rowid <= (SELECT rowid FROM conv_event WHERE id=?))`
			args = append(args, through)
		}
		query += ` ORDER BY rowid DESC`
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
	profile, err := r.memoryStore().Profile(ctx, memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}, time.Now().Unix(), 32, 4096, func(text string) int { return len(text) })
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
		hits, err := r.memoryStore().Recall(ctx, scope, memory.Query{ID: ref.ID, At: time.Now().Unix(), Limit: 1})
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

// memoryStore resolves evidence through the same ledger/material reader as turns.
func (r *conversationRuntime) memoryStore() *memory.Store {
	return memory.New(r.db).WithQuotes(ledger.New(r.db, r.materials).Citation)
}

// Topic memory is optional and collected only inside Atropos' deadline. The
// shared entry ranks ids; the memory reader resolves their current evidence.
func (r *conversationRuntime) topicPacket(ctx context.Context, input ledger.Event, at time.Time) (assemble.ContextPacket, error) {
	if r.channelBinding != nil && !r.channelBinding.Scope.Memory {
		return nil, nil
	}
	query := input.Body.DerivedText()
	// Recall accepts 512 UTF-8 bytes. Preserve the sentence's leading terms and
	// never split a rune when a person supplies a longer input.
	if len(query) > 512 {
		query = query[:512]
		for !utf8.ValidString(query) {
			query = query[:len(query)-1]
		}
	}
	hits, err := recall.Match(ctx, r.db, recall.Scope{Person: r.person, Agent: r.agent, Kinds: []recall.Kind{recall.Memory}, ExternalModel: true, At: at.Unix()}, recall.Query{Text: query, Limit: 12})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	items, err := r.memoryStore().Items(ctx, memory.RecallScope{Person: r.person, Agent: r.agent, ExternalModel: true}, ids, at.Unix(), false)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.Item, len(items))
	for _, hit := range items {
		byID[hit.ID] = hit.Item
	}
	var packet assemble.ContextPacket
	for rank, hit := range hits {
		item, ok := byID[hit.ID]
		if !ok || strconv.FormatInt(item.Revision, 10) != hit.Revision {
			continue
		}
		contextItem := topicContext(item, r.agent)
		contextItem.Rank = len(hits) - rank
		packet = append(packet, contextItem)
	}
	return packet, nil
}

func topicContext(item memory.Item, agent string) assemble.ContextItem {
	raw, _ := json.Marshal(struct {
		Kind        memory.Kind   `json:"kind"`
		Content     string        `json:"content"`
		Holder      memory.Holder `json:"holder"`
		SourceAgent string        `json:"source_agent"`
		Basis       memory.Basis  `json:"basis"`
	}{item.Kind, item.Content, item.Holder, item.SourceAgent, item.Basis})
	out := assemble.ContextItem{ID: "memory:" + item.ID, Revision: strconv.FormatInt(item.Revision, 10), AgentID: agent, Kind: assemble.Memory, Origin: "topic-memory:" + item.ID, Text: string(raw)}
	for _, ref := range item.Evidence {
		out.Evidence = append(out.Evidence, assemble.Ref{ID: ref.ID})
	}
	if item.Supersedes != "" {
		out.Supersedes = []string{"memory:" + item.Supersedes}
	}
	return out
}

func uniqueMemoryPacket(packet assemble.ContextPacket, minimum assemble.ContextPacket, profile []turn.Reference) assemble.ContextPacket {
	seen := map[string]bool{}
	for _, ref := range profile {
		seen["memory:"+ref.ID] = true
	}
	for _, item := range minimum {
		seen[item.ID] = true
	}
	out := make(assemble.ContextPacket, 0, len(packet))
	for _, item := range packet {
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	return out
}

func topicReferences(contexts []ledger.ContextSource) []turn.Reference {
	var refs []turn.Reference
	for _, item := range contexts {
		if item.Source == "topic-memory-v1" {
			refs = append(refs, turn.Reference{ID: strings.TrimPrefix(item.ID, "memory:"), Revision: item.Revision})
		}
	}
	return refs
}
