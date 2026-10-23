// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
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
	m.store = memory.New(db).WithQuotes(events.Citation)
	m.prepare = func(ctx context.Context, in memory.Write) (memory.ResolveEvidence, error) {
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
	m.ledger = func(ctx context.Context, scope memory.RecallScope, q memory.Query) ([]RecallSource, error) {
		// Raw conversation evidence has no grant for projection to another audience.
		if scope.Projection {
			return nil, nil
		}
		restricted, err := m.store.RestrictedEvents(ctx, scope)
		if err != nil {
			return nil, err
		}
		var ids []string
		err = db.Read(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, `SELECT id FROM retract_visible_event WHERE person_id=? AND agent_id=? AND speaker_kind='person' AND replay_of IS NULL AND unixepoch(occurred_at)<=?
    AND NOT EXISTS(SELECT 1 FROM ev_rel WHERE event_id=retract_visible_event.id AND kind='view_of')
    ORDER BY julianday(occurred_at) DESC,id`, scope.Person, scope.Agent, q.At)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			return nil, err
		}
		var sources []RecallSource
		for _, id := range ids {
			if restricted[id] {
				continue
			}
			event, err := events.Get(ctx, id)
			if errors.Is(err, ledger.ErrBytesUnavailable) && ctx.Err() == nil {
				continue
			}
			if err != nil {
				return nil, err
			}
			text := event.Body.DerivedText()
			if !strings.Contains(strings.ToLower(text), strings.ToLower(q.Text)) {
				continue
			}
			sources = append(sources, RecallSource{Citation: Citation{ID: "event:" + event.ID}, Text: text, Holder: memory.Holder(scope.Agent), Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact, SourceAgent: event.AgentID, OccurredAt: event.OccurredAt.Unix(), Quote: text}}})
			if len(sources) == q.Limit {
				break
			}
		}
		return sources, nil
	}
	return m
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
