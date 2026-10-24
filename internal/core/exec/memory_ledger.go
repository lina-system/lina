// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// NewMemory connects memory tools to canonical native ledger events. Large
// source bodies are verified through the ledger's materials adapter before
// entering the writer. Their immutable metadata and provenance are then read
// again in the memory transaction; no filesystem or model call holds the writer.
// Non-event evidence requires its owning flow's verified source adapter.
func NewMemory(db *canon.Store, materials ledger.Materials, aux Auxiliary) *Memory {
	events := ledger.New(db, materials)
	m := newMemory(db, nil, nil, aux)
	m.events = events
	m.store = memory.New(db).WithQuotes(events.Citation)
	m.prepare = func(ctx context.Context, in memory.Write) (memory.ResolveEvidence, error) {
		return PrepareMemoryEvidence(ctx, db, materials, in)
	}
	m.search = m.recallSources
	return m
}

// recallSources answers a person question from the same recall list as every
// other reader: memory items and events are matched and ranked together, and
// an event that is a returned item's evidence counts once as that item.
func (m *Memory) recallSources(ctx context.Context, scope memory.RecallScope, in PersonRecall) ([]RecallSource, error) {
	kinds := []recall.Kind{recall.Event, recall.Memory}
	// Raw conversation evidence has no grant for projection to another audience.
	if scope.Projection {
		kinds = []recall.Kind{recall.Memory}
	}
	query := recall.Scope{
		Person: scope.Person, Agent: scope.Agent, Kinds: kinds,
		CommonOnly: scope.CommonOnly, History: in.History, At: in.At,
		Projection: scope.Projection, ExternalModel: scope.ExternalModel,
	}
	if in.At > 0 {
		query.Until = time.Unix(in.At, 0).Add(time.Second)
	}
	hits, err := recall.Match(ctx, m.db, query, recall.Query{Text: in.Query, Limit: RecallItemLimit})
	if err != nil {
		return nil, err
	}
	var memoryIDs []string
	for _, hit := range hits {
		if hit.Kind == recall.Memory {
			memoryIDs = append(memoryIDs, hit.ID)
		}
	}
	items, err := m.store.Items(ctx, scope, memoryIDs, in.At, in.History)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.Hit, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	sources := make([]RecallSource, 0, len(hits))
	for _, hit := range hits {
		switch hit.Kind {
		case recall.Memory:
			item, ok := byID[hit.ID]
			if !ok {
				continue
			}
			sources = append(sources, RecallSource{Citation: Citation{ID: "memory:" + item.ID, Revision: item.Revision}, Text: item.Content, Holder: item.Holder, Evidence: item.Evidence, Older: item.Older})
		case recall.Event:
			event, err := m.events.Get(ctx, hit.ID)
			if errors.Is(err, ledger.ErrBytesUnavailable) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				return nil, err
			}
			text := event.Body.DerivedText()
			sources = append(sources, RecallSource{Citation: Citation{ID: "event:" + event.ID}, Text: text, Holder: memory.Holder(hit.Holder), Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact, SourceAgent: event.AgentID, OccurredAt: event.OccurredAt.Unix(), Quote: text}}})
		case recall.Summary, recall.Record:
			// recallSources only asks for events and memory items.
		}
	}
	return sources, nil
}

type transactionReader struct{ tx *canon.Tx }

func (r transactionReader) Read(_ context.Context, fn func(*canon.Tx) error) error { return fn(r.tx) }
func (r transactionReader) Write(context.Context, func(*canon.Tx) error) error {
	return canon.ErrReadOnly
}

type materialBodies map[ledger.Material]string

func (c materialBodies) Prepare(context.Context, secrets.Masked) (ledger.MaterialWrite, error) {
	return nil, memory.ErrEvidence
}
func (c materialBodies) Read(_ context.Context, ref ledger.Material) (string, error) {
	body, ok := c[ref]
	if !ok {
		return "", ledger.ErrBytesUnavailable
	}
	return body, nil
}

// Follow replay/view lineage without treating copies as independent evidence.
// Every link must retain the same speaker and owner; a forged relation cannot
// turn another speaker's words into a person event.
func canonicalEvent(ctx context.Context, events *ledger.Store, id string, cache materialBodies) (ledger.Event, error) {
	seen := map[string]bool{}
	var first *ledger.Event
	for {
		if seen[id] {
			return ledger.Event{}, memory.ErrEvidence
		}
		seen[id] = true
		event, err := events.Get(ctx, id)
		if err != nil {
			return ledger.Event{}, err
		}
		if cache != nil && event.Material.ID != "" {
			cache[event.Material] = event.Body.Text()
		}
		if first == nil {
			copy := event
			first = &copy
		} else if event.AgentID != first.AgentID || event.PersonID != first.PersonID || event.SpeakerKind != first.SpeakerKind {
			return ledger.Event{}, memory.ErrEvidence
		}
		next := event.ReplayOf
		for _, rel := range event.Relations {
			if rel.Kind == "view_of" {
				if next != "" && next != rel.TargetID {
					return ledger.Event{}, memory.ErrEvidence
				}
				next = rel.TargetID
			}
		}
		if next == "" {
			return event, nil
		}
		id = next
		if err := ctx.Err(); err != nil {
			return ledger.Event{}, errors.Join(memory.ErrEvidence, err)
		}
	}
}

// PrepareMemoryEvidence verifies material bytes before a caller enters the writer.
// The resolver rechecks the immutable ledger metadata in the caller transaction.
func PrepareMemoryEvidence(ctx context.Context, db *canon.Store, materials ledger.Materials, in memory.Write) (memory.ResolveEvidence, error) {
	events := ledger.New(db, materials)
	if db == nil {
		return nil, memory.ErrInvalid
	}
	cached := materialBodies{}
	for _, ref := range in.Item.Evidence {
		if ref.Kind != memory.Event || ref.Revision != 0 {
			return nil, memory.ErrEvidence
		}
		if _, err := canonicalEvent(ctx, events, ref.ID, cached); err != nil {
			return nil, err
		}
	}
	return func(ctx context.Context, tx *canon.Tx, ref memory.Evidence) (memory.Origin, error) {
		event, err := canonicalEvent(ctx, ledger.New(transactionReader{tx}, cached), ref.ID, nil)
		if err != nil {
			return memory.Origin{}, err
		}
		person := event.PersonID
		// Non-person events carry no person id. The authenticated flow supplies
		// the owning person; the source agent still must match at memory commit.
		if person == "" {
			person = in.Person
		}
		origin := memory.Origin{ID: event.ID, Person: person, Agent: event.AgentID, Speaker: event.SpeakerKind, Quote: event.Body.DerivedText(), OccurredAt: event.OccurredAt.Unix(), Scope: memory.Local}
		for _, span := range event.Spans {
			origin.Sensitivity = append(origin.Sensitivity, span.Sensitivity)
			if span.Sensitivity == "third_party" {
				origin.Scope = memory.Private
			}
		}
		return origin, nil
	}, nil
}
