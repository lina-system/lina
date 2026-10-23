// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/provider"
)

// Current-turn items are already durable in turn_items/turn_calls. They enter
// conv_event atomically with successful turn completion. Keep those two writer
// boundaries: the summary builder accounts for the conversation ledger only,
// while Session protects the exact live journal items from rewriting.
type conversationCompaction struct {
	logSizes     func(context.Context, compaction.Sizes) error
	initialItems int
	preparations []*compaction.Builder
	publisher    *compaction.Builder
	builder      *compaction.Builder
	pass         compaction.Pass
	view         compaction.View
	session      *compaction.Session
	pending      map[string]bool
	resolved     map[string]bool
}

func (c *conversationCompaction) canonical(segment compaction.PassSegment) compaction.PassSegment {
	kept := make([]compaction.Record, 0, len(segment.Protected))
	for _, record := range segment.Protected {
		if !c.pending[record.ID] {
			kept = append(kept, record)
		}
	}
	segment.Protected = kept
	return segment
}
func (c *conversationCompaction) summarize(ctx context.Context, segment compaction.PassSegment) (compaction.Text, error) {
	return c.builder.Summarize(ctx, c.canonical(segment))
}
func (c *conversationCompaction) prepared(ctx context.Context, segment compaction.PassSegment) (compaction.Text, bool, error) {
	segment = c.canonical(segment)
	for _, b := range c.preparations {
		if b.Model != c.builder.Model || b.GenerationRevision != c.builder.GenerationRevision {
			continue
		}
		text, ready, err := b.Prepared(ctx, segment)
		if err == nil && ready {
			c.publisher = b
			return text, true, nil
		}
	}
	return c.builder.Prepared(ctx, segment)
}
func (c *conversationCompaction) publish(ctx context.Context, segment compaction.PassSegment, text compaction.Text) (compaction.Published, error) {
	b := c.builder
	if c.publisher != nil {
		b = c.publisher
		c.publisher = nil
	}
	return b.Publish(ctx, c.canonical(segment), text)
}
func (c *conversationCompaction) transform(ctx context.Context, request provider.Request) (provider.Request, error) {
	var fitted provider.Request
	var err error
	if len(request.Items) == c.initialItems {
		fitted = request
	} else {
		fitted, err = c.session.Transform(ctx, request)
	}
	sizes, measureErr := c.session.Sizes()
	if measureErr == nil && c.logSizes != nil {
		measureErr = c.logSizes(ctx, sizes)
	}
	if errors.Is(err, compaction.ErrContinuation) {
		err = errors.Join(err, turn.ErrRequestTooLarge)
	}
	return fitted, errors.Join(err, measureErr)
}

// Bind every appended item to a committed journal row (or an admitted steering
// event) before Session can dispatch it. Matching consumes identities, so equal
// bodies from different events remain distinct and fabricated items fail closed.
func (s *turnState) resolveCompactionItems(ctx context.Context, items []provider.Item) ([]compaction.Record, error) {
	c, r := s.compaction, s.runtime
	if c.resolved == nil {
		c.resolved = map[string]bool{}
	}
	var candidates []compaction.Record
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT round,ordinal,item FROM turn_items WHERE turn_id=? ORDER BY round,ordinal", s.turnID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var round, index int
			var raw []byte
			if err := rows.Scan(&round, &index, &raw); err != nil {
				return err
			}
			id := fmt.Sprintf("%s:%d:%d", s.turnID, round+1, index)
			candidates = append(candidates, compaction.Record{ID: id, Item: raw})
			c.pending[id] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	batch, err := conversationTranscript(ctx, r.db, s.turnID, s.input, s.completed)
	if err != nil {
		return nil, err
	}
	for _, item := range batch {
		if item.Input.ScaffoldKind != "provider_item" {
			continue
		}
		candidates = append(candidates, compaction.Record{ID: item.Input.ID, Item: []byte(item.Input.Body)})
		c.pending[item.Input.ID] = true
	}
	var steering []string
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM conv_event WHERE conversation_id=? AND speaker_kind IN ('person','forwarded') AND rowid>(SELECT rowid FROM conv_event WHERE id=?) ORDER BY rowid`, r.conversation, s.input.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			steering = append(steering, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for _, id := range steering {
		e, err := r.journal().Get(ctx, id)
		if err != nil {
			return nil, err
		}
		projected, err := assemble.History([]ledger.Event{e}, r.conversation, r.agent)
		if err != nil {
			return nil, err
		}
		if len(projected) == 1 {
			candidates = append(candidates, compaction.Record{ID: id, Item: projected[0], User: e.SpeakerKind == guard.Person})
		}
	}
	var resolved []compaction.Record
	for _, item := range items {
		found := false
		for _, candidate := range candidates {
			if !c.resolved[candidate.ID] && bytes.Equal(item, candidate.Item) {
				resolved = append(resolved, candidate)
				c.resolved[candidate.ID] = true
				found = true
				break
			}
		}
		if !found {
			return nil, compaction.ErrPass
		}
	}
	return resolved, nil
}

// The auxiliary lease lasts until consumption, including background preparation.
type summaryStream struct {
	meter *summaryUsage
	provider.Stream
	release func()
}

func (s *summaryStream) Close() error {
	defer s.release()
	if s.meter != nil {
		s.meter.usage = s.Stream.Meta().Usage
	}
	return s.Stream.Close()
}
