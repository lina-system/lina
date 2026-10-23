// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// Check describes a completed reply, before presentation cuts. Identity comes
// from its source, never from model-supplied evaluation data.
type Check struct {
	ReplyType     string   `json:"reply_type,omitempty"`
	ShapeBreaches []string `json:"shape_breaches,omitempty"`
	Shapes        *Shapes  `json:"shapes,omitempty"`
	Budget        Budget   `json:"budget"`
	Count         Count    `json:"count"`
	BudgetBreach  bool     `json:"budget_breach"`
}

func (f *Filter) Check() Check {
	count := f.count(f.FullText())
	c := Check{Budget: f.budget, Count: count, BudgetBreach: count.exceeds(f.budget)}
	if f.shapes != nil {
		shapes := *f.shapes
		c.Shapes = &shapes
	}
	return c
}

// Admission belongs in the same batch as source and its reference card. Its
// deterministic identity prevents recording the same measurement twice.
func (c Check) Admission(source ledger.Input) ledger.Admission {
	body, _ := json.Marshal(c)
	return ledger.Admission{Entry: guard.EntrySystem, Input: ledger.Input{
		ID: "reply-check:" + source.ID, ParentID: source.ID,
		ConversationID: source.ConversationID, AgentID: source.AgentID,
		Surface: source.Surface, OccurredAt: source.OccurredAt,
		RecordedAt: source.RecordedAt, TimeQuality: source.TimeQuality,
		ScaffoldKind: "reply_check", Body: string(body),
	}}
}

type Reader interface {
	Read(context.Context, func(*canon.Tx) error) error
}

type Counts struct {
	Replies    int `json:"replies"`
	Budget     int `json:"budget"`
	Status     int `json:"status"`
	Comparison int `json:"comparison"`
	Candidates int `json:"candidates"`
	ThirdParty int `json:"third_party"`
}

// Breaches projects durable measurements for one speaking agent. Restoring a
// conversation is read-only and cannot increment these counts.
func Breaches(ctx context.Context, db Reader, agent string) (counts Counts, err error) {
	if db == nil || agent == "" {
		return counts, ErrConfig
	}
	err = db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(json_extract(e.body,'$.budget_breach')),0),
 coalesce(sum(EXISTS(SELECT 1 FROM json_each(e.body,'$.shape_breaches') WHERE value='status')),0),
 coalesce(sum(EXISTS(SELECT 1 FROM json_each(e.body,'$.shape_breaches') WHERE value='comparison')),0),
 coalesce(sum(EXISTS(SELECT 1 FROM json_each(e.body,'$.shape_breaches') WHERE value='candidates')),0),
 coalesce(sum(EXISTS(SELECT 1 FROM json_each(e.body,'$.shape_breaches') WHERE value='third_party')),0)
 FROM conv_event e JOIN conv_event source ON source.id=e.parent_id
 WHERE e.scaffold_kind='reply_check' AND e.speaker_kind='system'
 AND e.agent_id=? AND source.agent_id=e.agent_id AND source.speaker_kind='agent'`, agent).Scan(&counts.Replies, &counts.Budget, &counts.Status, &counts.Comparison, &counts.Candidates, &counts.ThirdParty)
	})
	return counts, err
}
