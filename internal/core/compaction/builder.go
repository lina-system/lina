// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/provider"
)

// Builder binds the fitting pass to the real summary store. Core supplies the
// conversation model, clock and unique ids. Generate is normally Auxiliary's
// Summarize. It runs outside the writer and receives canonical masked sources.
// One ready preparation is cached per conversation; reads never wait for a
// preparation in progress. Publication rechecks its exact frontier and epoch.
type Builder struct {
	Store               *Store
	Events              *ledger.Store
	Agent, Conversation string
	Model               provider.ModelRef
	Manager             bool
	// CurrentInput identifies the retained input of a conversation turn. Later
	// queued person/forwarded inputs count in the publication frontier, but are
	// not part of this turn's request or summary sources. Empty disables this
	// conversation-specific boundary for other builder callers.
	CurrentInput string
	// GenerationRevision binds reasoning effort and other summary route settings.
	GenerationRevision string
	// PreparationJob names the Core-registered Moirai job for this conversation.
	PreparationJob string
	Now            func() time.Time
	NextID         func() string
	Generate       func(context.Context, PassSegment) (Text, error)
	mu             sync.Mutex
	ready          *preparedPass
	workerReady    *preparationResult
}
type preparedPass struct {
	key           [32]byte
	sourceKey     [32]byte
	configuration [32]byte
	proposal      Proposal
	text          Text
}

func segmentKey(segment PassSegment) [32]byte {
	raw, _ := json.Marshal(segment)
	return sha256.Sum256(raw)
}
func (b *Builder) capture(ctx context.Context, segment PassSegment) (Proposal, PassSegment, error) {
	if b.Store == nil || b.Events == nil || b.Agent == "" || b.Conversation == "" || b.Model == "" || b.Now == nil || b.NextID == nil || b.Generate == nil {
		return Proposal{}, PassSegment{}, ErrPass
	}
	p := Proposal{ID: b.NextID()}
	resolved := PassSegment{}
	err := b.Store.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		p.Expected, err = readFrontier(ctx, tx, b.Conversation)
		if err != nil {
			return err
		}
		if p.Expected.Root != segment.RootID {
			return ErrStaleFrontier
		}
		inherited := map[string]bool{}
		if segment.RootID != "" {
			node, err := readNode(ctx, tx, segment.RootID)
			if err != nil {
				return err
			}
			if node.Conversation != b.Conversation || node.Status != "ready" || node.Epoch != p.Expected.Epoch {
				return ErrUnavailable
			}
			var text Text
			if json.Unmarshal([]byte(node.Output.Text), &text) != nil || !text.valid() {
				return ErrSummary
			}
			resolved.RootID = node.ID
			resolved.Root = SummaryItem(text, b.Manager)
			p.Children = []string{node.ID}
			coverage, err := treeCoverage(ctx, tx, node.ID)
			if err != nil {
				return err
			}
			for _, c := range coverage {
				inherited[c.EventID] = true
			}
		}
		wanted := map[string]bool{}
		kept := map[string]bool{}
		for _, r := range segment.Records {
			if r.ID == "" || wanted[r.ID] || inherited[r.ID] {
				return ErrCoverage
			}
			wanted[r.ID] = true
		}
		for _, r := range append(copyRecords(segment.Verbatim), segment.Protected...) {
			if r.ID == "" || wanted[r.ID] || inherited[r.ID] || kept[r.ID] {
				return ErrCoverage
			}
			kept[r.ID] = true
		}
		queued := map[string]bool{}
		if b.CurrentInput != "" {
			if !kept[b.CurrentInput] {
				return ErrCoverage
			}
			rows, err := tx.QueryContext(ctx, `SELECT id FROM conv_event WHERE conversation_id=? AND speaker_kind IN ('person','forwarded') AND rowid > (SELECT rowid FROM conv_event WHERE id=? AND conversation_id=?)`, b.Conversation, b.CurrentInput, b.Conversation)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				if wanted[id] || inherited[id] {
					rows.Close()
					return ErrCoverage
				}
				queued[id] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		all, err := events(ctx, tx, b.Conversation)
		if err != nil {
			return err
		}
		tail := false
		for _, e := range all {
			var owner string
			if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM conv_event WHERE id=?`, e.id).Scan(&owner); err != nil {
				return err
			}
			if owner != b.Agent {
				return ErrExpand
			}
			switch {
			case inherited[e.id]:
			case wanted[e.id]:
				if !e.visible {
					return ErrUnavailable
				}
				p.Cover = append(p.Cover, Coverage{EventID: e.id})
				delete(wanted, e.id)
			case kept[e.id]:
				if !e.visible {
					return ErrUnavailable
				}
				p.Verbatim = append(p.Verbatim, e.id)
				delete(kept, e.id)
				tail = true
			case queued[e.id] && skipReason(e) == "":
				// Keep the id for exact coverage without reading its body into
				// the resolved segment or changing the caller's request view.
				p.Verbatim = append(p.Verbatim, e.id)
				tail = true
			default:
				c := Coverage{EventID: e.id, Reason: skipReason(e)}
				if c.Reason == "" {
					return ErrCoverage
				}
				if tail {
					p.TailSkips = append(p.TailSkips, c)
				} else {
					p.Cover = append(p.Cover, c)
				}
			}
		}
		if len(wanted) != 0 || len(kept) != 0 {
			return ErrCoverage
		}
		_, err = validateCoverage(ctx, tx, p)
		return err
	})
	if err != nil {
		return Proposal{}, PassSegment{}, err
	}
	for _, c := range p.Cover {
		if c.Reason != "" {
			continue
		}
		e, err := b.Events.Get(ctx, c.EventID)
		if err != nil {
			return Proposal{}, PassSegment{}, err
		}
		if e.AgentID != b.Agent || e.ConversationID != b.Conversation {
			return Proposal{}, PassSegment{}, ErrExpand
		}
		raw, _ := json.Marshal(struct {
			Speaker guard.SpeakerKind `json:"speaker_kind"`
			Origin  ledger.Origin     `json:"origin"`
			Body    string            `json:"body"`
		}{e.SpeakerKind, e.Origin, e.Body.DerivedText()})
		resolved.Records = append(resolved.Records, Record{ID: e.ID, Item: raw, User: e.SpeakerKind == guard.Person})
	}
	return p, resolved, nil
}
func (b *Builder) Summarize(ctx context.Context, segment PassSegment) (Text, error) {
	p, resolved, err := b.capture(ctx, segment)
	if err != nil {
		return Text{}, err
	}
	text, err := b.Generate(ctx, resolved)
	if err != nil {
		return Text{}, err
	}
	if !text.valid() {
		return Text{}, ErrSummary
	}
	raw, _ := json.Marshal(text)
	p.Output = Output{Model: string(b.Model), PromptVersion: PromptVersion, BuiltAt: b.Now(), Text: string(raw), Dropped: text.Omitted}
	if !validOutput(p.Output) {
		return Text{}, ErrSummary
	}
	// Validation here avoids caching a stale generation; publication still
	// validates again in its writer transaction.
	if err = b.Store.db.Read(ctx, func(tx *canon.Tx) error { return checkFrontier(ctx, tx, p.Expected) }); err != nil {
		return Text{}, err
	}
	b.mu.Lock()
	b.ready = &preparedPass{key: segmentKey(segment), sourceKey: segmentKey(resolved), configuration: b.configuration(), proposal: p, text: text}
	b.mu.Unlock()
	return text, nil
}
func (b *Builder) Prepared(ctx context.Context, segment PassSegment) (Text, bool, error) {
	b.mu.Lock()
	ready := b.ready
	b.mu.Unlock()
	if ready == nil {
		var err error
		ready, err = b.workerPreparation(ctx)
		if err != nil {
			return Text{}, false, err
		}
		if ready == nil {
			return Text{}, false, nil
		}
	}
	p, resolved, err := b.capture(ctx, segment)
	if err != nil {
		return Text{}, false, err
	}
	if p.Expected.Epoch != ready.proposal.Expected.Epoch || p.Expected.Root != ready.proposal.Expected.Root || p.Expected.Revision != ready.proposal.Expected.Revision || ready.configuration != b.configuration() || segmentKey(resolved) != ready.sourceKey {
		return Text{}, false, nil
	}
	// New current-turn events can be retained beside an unchanged preparation.
	// Rebase its publication fence and verbatim ids, never its summarized sources.
	p.Output = ready.proposal.Output
	refreshed := &preparedPass{key: segmentKey(segment), sourceKey: ready.sourceKey, configuration: ready.configuration, proposal: p, text: ready.text}
	b.mu.Lock()
	if b.ready != nil && b.ready != ready {
		b.mu.Unlock()
		return Text{}, false, nil
	}
	b.ready = refreshed
	b.mu.Unlock()
	return refreshed.text, true, nil
}
func (b *Builder) Publish(ctx context.Context, segment PassSegment, text Text) (Published, error) {
	b.mu.Lock()
	ready := b.ready
	b.mu.Unlock()
	if ready == nil || ready.key != segmentKey(segment) || ready.text != text || ready.configuration != b.configuration() {
		return Published{}, ErrSummary
	}
	p := ready.proposal
	node, err := b.Store.publish(ctx, p, func(tx *canon.Tx) error {
		first, last := "", ""
		if len(p.Cover) > 0 {
			first = p.Cover[0].EventID
			last = p.Cover[len(p.Cover)-1].EventID
		}
		body, _ := json.Marshal(struct {
			Kind, Node, FirstEvent, LastEvent string
			Events                            int
			Children                          []string
		}{"summary", p.ID, first, last, len(p.Cover), p.Children})
		_, err := ledger.New(continuationTx{tx}, nil).Admit(ctx, guard.EntrySystem, ledger.Input{ID: "compaction:" + p.ID, ConversationID: b.Conversation, ParentID: p.Expected.LastEvent, AgentID: b.Agent, Surface: ledger.Surface{Kind: "app"}, ScaffoldKind: "compaction_recorded", OccurredAt: p.Output.BuiltAt, RecordedAt: p.Output.BuiltAt, TimeQuality: "recorded", Body: string(body)})
		return err
	})
	if err != nil {
		return Published{}, err
	}
	b.mu.Lock()
	if b.ready == ready {
		b.ready = nil
	}
	b.mu.Unlock()
	return Published{ID: node.ID, Item: SummaryItem(text, b.Manager)}, nil
}
