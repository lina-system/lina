// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Open recovers removals before returning. Core must call it before serving
// reads and keeps one store for every kind, because the removal list is a single
// append-only file with one sequence. Memory items and messenger records are
// accepted; a native conversation's deletion and the source registry lifecycle
// have their own owners.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	return openStore(ctx, cfg, projection{
		resolve: func(ctx context.Context, tx *canon.Tx, ref Ref, person string) (Target, error) {
			switch ref.Kind {
			case Memory:
				return resolveMemory(ctx, tx, ref, person)
			case Event:
				return resolveEvent(ctx, tx, ref, person)
			case Source:
				// A source disconnect is the registry's own removal; this store
				// only journals and replays it.
				return Target{}, ErrInvalid
			default:
				return Target{}, ErrInvalid
			}
		},
		expand: func(ctx context.Context, tx *canon.Tx, e entry) ([]Target, error) {
			events := make([]Target, 0, len(e.Targets))
			for _, target := range e.Targets {
				if target.Kind == Memory {
					continue
				}
				events = append(events, target)
			}
			if len(events) == len(e.Targets) {
				return expandEvents(ctx, tx, e, e.Targets)
			}
			memory := e
			memory.Targets = nil
			for _, target := range e.Targets {
				if target.Kind == Memory {
					memory.Targets = append(memory.Targets, target)
				}
			}
			expanded, err := expandMemory(ctx, tx, memory)
			if err != nil {
				return nil, err
			}
			return append(expanded, events...), nil
		},
		apply: func(ctx context.Context, tx *canon.Tx, e entry, target Target) error {
			if target.Kind == Event {
				return applyEvent(ctx, tx, e, target)
			}
			return applyMemory(ctx, tx, e, target)
		},
	})
}

// Resolve the whole supersession chain while the writer is fenced, before the
// single durable journal append. An older generation may contain only the first
// item, so replay must not depend on finding the replacement or its links there.
func expandMemory(ctx context.Context, tx *canon.Tx, e entry) ([]Target, error) {
	if e.Action == restore {
		// Recover only the requested item; its superseded history stays forgotten.
		return e.Targets, nil
	}
	targets := slices.Clone(e.Targets)
	seen := map[string]bool{}
	for _, target := range targets {
		seen[target.ID] = true
	}
	for _, root := range e.Targets {
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE chain(id) AS (
 SELECT supersedes FROM memory_item WHERE id=?
 UNION SELECT m.supersedes FROM memory_item m JOIN chain c ON m.id=c.id
) SELECT id FROM chain WHERE id<>'' ORDER BY id`, root.ID)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			var state, person string
			err := tx.QueryRowContext(ctx, "SELECT state,person FROM retract_target WHERE kind='memory' AND id=?", id).Scan(&state, &person)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if person != "" && person != e.Person {
				return nil, ErrInvalid
			}
			// Existing removals already travel with every generation. Do not reset
			// their trash deadline or try to resolve content that was erased.
			if state == "erased" || state == "trashed" && e.Action == forget {
				continue
			}
			target, err := resolveMemory(ctx, tx, Ref{Kind: Memory, ID: id}, e.Person)
			if err != nil {
				return nil, err
			}
			if target.Holder != root.Holder {
				return nil, ErrInvalid
			}
			if err := transition(ctx, tx, target, e); err != nil {
				return nil, err
			}
			targets = append(targets, target)
		}
	}
	return targets, nil
}

type memoryRecord struct {
	Person      string `json:"person"`
	SourceAgent string `json:"source_agent"`
	Evidence    []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"evidence"`
}

func resolveMemory(ctx context.Context, tx *canon.Tx, ref Ref, person string) (Target, error) {
	if ref.Kind != Memory || ref.ID == "" {
		return Target{}, ErrInvalid
	}
	target := Target{Kind: Memory, ID: ref.ID}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT holder,record FROM memory_item WHERE id=? AND person=?", ref.ID, person).Scan(&target.Holder, &raw); err != nil {
		return Target{}, err
	}
	var current memoryRecord
	if err := json.Unmarshal(raw, &current); err != nil {
		return Target{}, err
	}
	target.Agent = current.SourceAgent
	// All revisions and item-evidence ancestors contribute extraction exclusions.
	// Reading only the current record would lose an earlier revision's sources.
	pending := []string{ref.ID}
	seen := map[string]bool{}
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT person FROM memory_item WHERE id=?
 UNION ALL SELECT person FROM retract_target WHERE kind='memory' AND id=? AND state='erased' LIMIT 1`, id, id).Scan(&owner); err != nil {
			return Target{}, err
		}
		if owner != person {
			return Target{}, ErrInvalid
		}
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT target_kind,target_id FROM cite WHERE view_kind='memory' AND view_id=?", id)
		if err != nil {
			return Target{}, err
		}
		type reference struct{ kind, id string }
		var refs []reference
		for rows.Next() {
			var ref reference
			if err := rows.Scan(&ref.kind, &ref.id); err != nil {
				rows.Close()
				return Target{}, err
			}
			refs = append(refs, ref)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return Target{}, err
		}
		for _, ref := range refs {
			switch ref.kind {
			case "event":
				var conversation string
				if err := tx.QueryRowContext(ctx, "SELECT conversation_id FROM conv_event WHERE id=?", ref.id).Scan(&conversation); err != nil {
					return Target{}, err
				}
				target.Events = append(target.Events, ref.id)
				target.Conversations = append(target.Conversations, conversation)
			case "memory":
				pending = append(pending, ref.id)
			}
		}
	}
	slices.Sort(target.Events)
	target.Events = slices.Compact(target.Events)
	slices.Sort(target.Conversations)
	target.Conversations = slices.Compact(target.Conversations)
	return target, nil
}

func applyMemory(ctx context.Context, tx *canon.Tx, e entry, target Target) error {
	if target.Kind != Memory {
		return ErrInvalid
	}
	switch e.Action {
	case forget, restore:
		state := "trashed"
		if e.Action == restore {
			state = "active"
		}
		_, err := tx.ExecContext(ctx, "UPDATE memory_item SET state=?,record=json_set(record,'$.state',?) WHERE id=? AND person=?", state, state, target.ID, e.Person)
		return err
	case erase:
		if _, err := tx.ExecContext(ctx, "DELETE FROM memory_revision WHERE item_id=?", target.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM memory_item WHERE id=? AND person=?", target.ID, e.Person)
		return err
	case disconnect:
		return ErrInvalid
	}
	return ErrInvalid
}
