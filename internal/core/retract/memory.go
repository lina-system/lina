// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Open recovers memory removals before returning. Core must call it before
// serving reads. Conversation-content deletion and source registry lifecycle
// have their own owners; this coordinator accepts memory targets.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	return openStore(ctx, cfg, projection{resolve: resolveMemory, apply: applyMemory})
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
		rows, err := tx.QueryContext(ctx, "SELECT record FROM memory_revision WHERE item_id=? ORDER BY revision", id)
		if err != nil {
			return Target{}, err
		}
		var records []memoryRecord
		for rows.Next() {
			var raw []byte
			var record memoryRecord
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return Target{}, err
			}
			if err := json.Unmarshal(raw, &record); err != nil {
				rows.Close()
				return Target{}, err
			}
			records = append(records, record)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return Target{}, err
		}
		for _, record := range records {
			if record.Person != person {
				return Target{}, ErrInvalid
			}
			for _, e := range record.Evidence {
				switch e.Kind {
				case "event":
					var conversation string
					if err := tx.QueryRowContext(ctx, "SELECT conversation_id FROM conv_event WHERE id=?", e.ID).Scan(&conversation); err != nil {
						return Target{}, err
					}
					target.Events = append(target.Events, e.ID)
					target.Conversations = append(target.Conversations, conversation)
				case "item":
					pending = append(pending, e.ID)
				}
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
