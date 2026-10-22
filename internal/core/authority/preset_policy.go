// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

var ErrPresetLimit = errors.New("preset_effect_limit")

// Limits are ceilings, not grants. Combining delegations only adds ceilings.
// Preset identity is read from canon; names, persona layers, approval answers,
// accepted drafts and channel bindings do not participate.
type Limits struct {
	AdviceOnly bool `json:"advice_only,omitempty"`
	DraftOnly  bool `json:"draft_only,omitempty"`
}

func (l Limits) Intersect(other Limits) Limits {
	return Limits{l.AdviceOnly || other.AdviceOnly, l.DraftOnly || other.DraftOnly}
}

func (l Limits) Allows(class tools.EffectClass) bool {
	if !l.AdviceOnly && !l.DraftOnly {
		return true
	}
	// Relay admission is safe only when its Core handler binds these limits to
	// the receiving task before scheduling it. It grants no execution authority.
	return class == tools.Research || class == tools.Calculation || class == tools.PrivateDraft || class == tools.LocalWrite || class == tools.RelayEffect
}

func presetLimits(ctx context.Context, tx *canon.Tx, agent string) (Limits, error) {
	var preset string
	var trashed int64
	err := tx.QueryRowContext(ctx, "SELECT preset,trashed_at FROM agents WHERE id=?", agent).Scan(&preset, &trashed)
	if errors.Is(err, sql.ErrNoRows) {
		// Core also admits non-persona principals (e.g. an installed executor).
		// Their identity and authority still have to pass grant.Check.
		return Limits{}, nil
	}
	if err != nil {
		return Limits{}, err
	}
	if trashed != 0 {
		return Limits{}, ErrPresetLimit
	}
	return Limits{AdviceOnly: preset == "investing", DraftOnly: preset == "legal"}, nil
}

func taskLimits(ctx context.Context, tx *canon.Tx, person, agent, task string) (Limits, error) {
	limits, err := presetLimits(ctx, tx, agent)
	if err != nil {
		return Limits{}, err
	}
	var owner, receiver string
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT person,receiver,limits FROM authority_delegations WHERE task_id=?", task).Scan(&owner, &receiver, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return limits, nil
	}
	if err != nil {
		return Limits{}, err
	}
	if owner != person || receiver != agent {
		return Limits{}, ErrPresetLimit
	}
	var inherited Limits
	if err = json.Unmarshal(raw, &inherited); err != nil {
		return Limits{}, err
	}
	return limits.Intersect(inherited), nil
}

// BindDelegation is called only by Core composition with authenticated origin
// identities, never with identity fields accepted from model tool arguments.
// The snapshot survives origin deletion, restart and any later handoff.
func (s *Service) BindDelegation(ctx context.Context, person, source, sourceTask, receiver, task string) error {
	if person == "" || source == "" || receiver == "" || sourceTask == "" || task == "" || sourceTask == task {
		return tools.ErrInvalid
	}
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		limits, err := taskLimits(ctx, tx, person, source, sourceTask)
		if err != nil {
			return err
		}
		other, err := presetLimits(ctx, tx, receiver)
		if err != nil {
			return err
		}
		limits = limits.Intersect(other)
		raw, _ := json.Marshal(limits)
		_, err = tx.ExecContext(ctx, "INSERT INTO authority_delegations(task_id,person,source,source_task,receiver,limits) VALUES(?,?,?,?,?,?) ON CONFLICT(task_id) DO NOTHING", task, person, source, sourceTask, receiver, raw)
		if err != nil {
			return err
		}
		var matches int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM authority_delegations WHERE task_id=? AND person=? AND source=? AND source_task=? AND receiver=? AND limits=?", task, person, source, sourceTask, receiver, raw).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			return tools.ErrKeyConflict
		}
		return nil
	})
}

func refusal(limits Limits, class tools.EffectClass) tools.Result {
	boundary := "private drafts only; external or binding execution is unavailable"
	if limits.AdviceOnly {
		boundary = "investment advice only; orders and external execution are unavailable"
	}
	body, _ := json.Marshal(map[string]any{"code": ErrPresetLimit.Error(), "effect_class": class, "limits": limits, "boundary": boundary})
	return tools.Result{State: tools.Refused, Body: body}
}

// PolicyRequest carries trusted Core routing facts for an executor that owns
// its own grant/receipt protocol (workers and merge service). This ceiling check
// grants nothing. Refusals use the same effect ledger as registry dispatch.
type PolicyRequest struct {
	Key, Person, Agent, TaskID, Tool, Target string
	Class                                    tools.EffectClass
	Arguments                                json.RawMessage
}

func CheckPreset(ctx context.Context, db *canon.Store, epoch uint64, request PolicyRequest) error {
	if db == nil || epoch == 0 || request.Key == "" || request.Person == "" || request.Agent == "" || request.Tool == "" || request.Target == "" || !json.Valid(request.Arguments) {
		return tools.ErrInvalid
	}
	var denied error
	err := db.Write(ctx, func(tx *canon.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&current); err != nil {
			return err
		}
		if current != fmt.Sprint(epoch) {
			return tools.ErrInvalid
		}
		limits, err := taskLimits(ctx, tx, request.Person, request.Agent, request.TaskID)
		if err != nil {
			return err
		}
		if limits.Allows(request.Class) {
			return nil
		}
		denied = ErrPresetLimit
		args, err := canonicalArguments(request.Arguments)
		if err != nil {
			return err
		}
		e := Effect{Key: request.Key, Person: request.Person, Agent: request.Agent, TaskID: request.TaskID, Tool: request.Tool, Target: request.Target, Class: request.Class, ArgumentHash: digest(args), Epoch: epoch, State: tools.Refused, Basis: ErrPresetLimit.Error(), Result: refusal(limits, request.Class)}
		identity := e
		identity.Epoch = 0
		raw, _ := json.Marshal(identity)
		fingerprint := digest(raw)
		_, old, _, err := readEffect(ctx, tx, e.Key)
		if err == nil {
			if old != fingerprint {
				return tools.ErrKeyConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return insertEffect(ctx, tx, e, fingerprint, false)
	})
	if err != nil {
		return err
	}
	return denied
}
