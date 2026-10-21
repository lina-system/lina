// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// ContinuationLedger commits the end marker and continuation reference in one
// fenced canon transaction. Core supplies its turn writer through End; it must
// validate the ended turn's ownership and input before updating it. No request,
// packet or duplicate input body is stored. Ids and time come from Core.
type ContinuationLedger struct {
	DB                                    ledger.Canon
	Agent, Conversation, EventID, SizesID string
	At                                    time.Time
	End                                   func(context.Context, *canon.Tx, string) error
}
type continuationTx struct{ tx *canon.Tx }

func (t continuationTx) Read(_ context.Context, fn func(*canon.Tx) error) error  { return fn(t.tx) }
func (t continuationTx) Write(_ context.Context, fn func(*canon.Tx) error) error { return fn(t.tx) }
func (j ContinuationLedger) Record(ctx context.Context, record ContinuationRecord) error {
	if j.DB == nil || j.End == nil || j.Agent == "" || j.Conversation == "" || j.EventID == "" || record.EndedTurn == "" || record.InputID == "" {
		return ErrPass
	}
	return j.DB.Write(ctx, func(tx *canon.Tx) error {
		events := ledger.New(continuationTx{tx}, nil)
		// Ownership is checked before the caller's turn mutation. Input bodies may
		// be externalized, so metadata lookup never fetches their bytes.
		var agent, conversation string
		if err := tx.QueryRowContext(ctx, `SELECT agent_id,conversation_id FROM conv_event WHERE id=?`, record.InputID).Scan(&agent, &conversation); err != nil {
			return err
		}
		if agent != j.Agent || conversation != j.Conversation {
			return ErrPass
		}
		if err := j.End(ctx, tx, record.EndedTurn); err != nil {
			return err
		}
		body, _ := json.Marshal(struct {
			Kind      string `json:"kind"`
			EndedTurn string `json:"ended_turn"`
			InputID   string `json:"input_id"`
		}{"continuation", record.EndedTurn, record.InputID})
		_, err := events.Admit(ctx, guard.EntrySystem, ledger.Input{ID: j.EventID, ParentID: record.InputID, AgentID: j.Agent, ConversationID: j.Conversation, Surface: ledger.Surface{Kind: "app"}, ScaffoldKind: "compaction_recorded", OccurredAt: j.At, RecordedAt: j.At, TimeQuality: "recorded", Body: string(body)})
		return err
	})
}
func (j ContinuationLedger) Log(ctx context.Context, sizes Sizes) error {
	if j.DB == nil || j.SizesID == "" {
		return ErrPass
	}
	body, _ := json.Marshal(struct {
		Kind  string `json:"kind"`
		Sizes Sizes  `json:"sizes"`
	}{"continuation_sizes", sizes})
	_, err := ledger.New(j.DB, nil).Admit(ctx, guard.EntrySystem, ledger.Input{ID: j.SizesID, ParentID: j.EventID, AgentID: j.Agent, ConversationID: j.Conversation, Surface: ledger.Surface{Kind: "app"}, ScaffoldKind: "compaction_recorded", OccurredAt: j.At, RecordedAt: j.At, TimeQuality: "recorded", Body: string(body)})
	return err
}
