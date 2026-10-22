// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

// RunEffectSource projects independent effect questions after lane delivery.
// Answers still use the authenticated question card, never the waiting service.
type RunEffectSource struct{ AgentName func(string) string }

func (RunEffectSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.id FROM background_runs r
		JOIN lane_notices n ON n.id=r.id||':effects'
		AND n.conversation=json_extract(r.record,'$.Origin.Conversation')
		WHERE n.delivered=1 AND json_extract(r.record,'$.Origin.Person')=?
		AND json_extract(r.record,'$.EffectsQuestion') IS NOT NULL
		AND json_extract(r.record,'$.EffectsQuestion.Answer') IS NULL ORDER BY r.seq`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s RunEffectSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	if !strings.HasSuffix(id, ":effects") {
		return Item{}, ErrInvalid
	}
	r, err := readRunInput(ctx, tx, strings.TrimSuffix(id, ":effects"))
	if err != nil {
		return Item{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT record FROM lane_notices WHERE id=? AND conversation=? AND delivered=1", id, r.Origin.Conversation).Scan(&raw); err != nil {
		return Item{}, err
	}
	var notice struct{ Summary string }
	if err = json.Unmarshal(raw, &notice); err != nil {
		return Item{}, err
	}
	finished := r.EffectsQuestion == nil || r.EffectsQuestion.Answer != nil
	v := view.Waiting{Kind: view.Decide, Sentence: notice.Summary, Context: r.Brief.Goal, Answers: []string{"open"}, Effort: view.FiveMinutes}
	if s.AgentName != nil {
		v.AgentName = s.AgentName(r.Origin.Agent)
		finished = finished || v.AgentName == ""
	}
	return Item{Person: r.Origin.Person, Agent: r.Origin.Agent, Revision: 1, View: v, Reason: OutsideEffect, Blocking: true, Finished: finished, Requested: time.Unix(0, r.Created)}, nil
}

func (RunEffectSource) Done(context.Context, *canon.Tx, string) error { return nil }
