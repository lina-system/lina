// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

var ErrExpand = errors.New("expansion_unavailable")

// Expansion resolves exact ledger bodies, including immutable materials. The
// bound agent may read all its conversations; summary text is never evidence.
type Expansion struct {
	DB     ledger.Canon
	Events *ledger.Store
	// NodeEvents checks node ownership, validity and coverage, returning exact
	// event ids in ledger order and the next offset (zero marks the final page).
	NodeEvents func(context.Context, string, string, int, int) ([]string, int, error)
}
type ExpandedEvent struct {
	ID           string            `json:"id"`
	Conversation string            `json:"conversation"`
	Speaker      guard.SpeakerKind `json:"speaker_kind"`
	Body         string            `json:"body"`
	Material     ledger.Material   `json:"material,omitempty"`
}
type ExpansionPage struct {
	Events []ExpandedEvent `json:"events"`
	Next   int             `json:"next,omitempty"`
}

func (e Expansion) epoch(ctx context.Context) (uint64, error) {
	var epoch uint64
	err := e.DB.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT epoch FROM retract_meta WHERE singleton=1`).Scan(&epoch)
	})
	return epoch, err
}

// Read matches exec.ExpandRead without a flow-to-flow import. Ref is explicitly
// node:<id> or event:<id>; event pagination treats the full body as one record.
// Missing material bytes fail the whole page, never return a preview as exact.
func (e Expansion) Read(ctx context.Context, agent, ref string, offset, limit int) (json.RawMessage, error) {
	if e.DB == nil || e.Events == nil || agent == "" || offset < 0 || limit < 1 || limit > 1000 {
		return nil, ErrExpand
	}
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" {
		return nil, ErrExpand
	}
	epoch, err := e.epoch(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	next := 0
	switch kind {
	case "node":
		if e.NodeEvents == nil {
			return nil, ErrExpand
		}
		ids, next, err = e.NodeEvents(ctx, agent, id, offset, limit)
		if err != nil {
			return nil, err
		}
	case "event":
		ids = []string{id}
	default:
		return nil, ErrExpand
	}
	if len(ids) > limit || next < 0 || next != 0 && next <= offset {
		return nil, ErrExpand
	}
	page := ExpansionPage{Events: []ExpandedEvent{}, Next: next}
	for _, id := range ids {
		var visible int
		err = e.DB.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT count(*) FROM retract_visible_event WHERE id=? AND agent_id=?`, id, agent).Scan(&visible)
		})
		if err != nil {
			return nil, err
		}
		if visible != 1 {
			return nil, ErrExpand
		}
		event, err := e.Events.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if event.AgentID != agent {
			return nil, ErrExpand
		}
		if kind == "event" && offset > 0 {
			continue
		}
		page.Events = append(page.Events, ExpandedEvent{ID: event.ID, Conversation: event.ConversationID, Speaker: event.SpeakerKind, Body: event.Body.Text(), Material: event.Material})
	}
	current, err := e.epoch(ctx)
	if err != nil {
		return nil, err
	}
	if current != epoch {
		return nil, ErrExpand
	}
	return json.Marshal(page)
}
