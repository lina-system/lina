// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/kit/loop"
)

// EffectQuestion keeps the remainder that cannot run under a read-only scope
// visible to the conversation owner. It grants no authority: the work engine
// must bind concrete future effects to its normal authorization/approval path.
// Unknown receipts are evidence to reconcile, never calls to execute again.
type EffectQuestion struct {
	RunID, Turn, State string
	Effects            []budget.Effect
}

// StartFromTurn durably admits the whole remaining request with gathered state.
// Like Start, it returns a handle before any model call. The caller persists or
// publishes that handle before Release. A repeated promotion returns the same
// run, including after restart; changing its gathered state is refused.
func (s *Service) StartFromTurn(ctx context.Context, o Origin, b Brief, h budget.Handoff) (budget.Handle, error) {
	if o.Job != "" || h.Turn == "" || h.Turn != o.Turn || h.Input == "" && len(h.Items) == 0 {
		return budget.Handle{}, ErrInvalid
	}
	for _, item := range h.Items {
		if !json.Valid(item) {
			return budget.Handle{}, ErrInvalid
		}
	}
	for _, effect := range h.Effects {
		if effect.CallID == "" || effect.Key == "" || effect.Tool == "" || effect.Code != loop.OK && effect.Code != loop.Unknown {
			return budget.Handle{}, ErrInvalid
		}
	}
	data, err := json.Marshal(h)
	if err != nil {
		return budget.Handle{}, ErrInvalid
	}
	var detached budget.Handoff
	if err := json.Unmarshal(data, &detached); err != nil {
		return budget.Handle{}, ErrInvalid
	}
	// Promotion gets only reads from the caller's allowlist. It never inherits
	// an effect-capable conversation tool, scratch tool or hand-off tool.
	allow := make([]string, 0, len(b.Allow))
	for _, name := range b.Allow {
		if tool, ok := s.cfg.Tools[name]; ok && tool.Kind == Read && name != "start_run" && name != "dispatch_work" {
			allow = append(allow, name)
		}
	}
	b.Allow = allow
	id, state, err := s.start(ctx, o, b, &detached)
	if err != nil {
		return budget.Handle{}, err
	}
	return budget.Handle{ID: id, State: string(state)}, nil
}

func samePromotion(r Record, o Origin) bool {
	return r.Promotion != nil && r.Origin.Person == o.Person && r.Origin.Agent == o.Agent && r.Origin.Conversation == o.Conversation && r.Origin.Turn == o.Turn
}

func sameGathered(r Record, b Brief, h *budget.Handoff) bool {
	previous, _ := json.Marshal(struct {
		Brief Brief
		State *budget.Handoff
	}{r.Brief, r.Promotion})
	current, _ := json.Marshal(struct {
		Brief Brief
		State *budget.Handoff
	}{b, h})
	return bytes.Equal(previous, current)
}
