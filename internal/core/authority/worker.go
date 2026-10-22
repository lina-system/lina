// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

const workerRelayPlugin = "worker-relay"

func workerRequest(in Approval) (Approval, string, error) {
	for _, value := range []string{in.EffectKey, in.Person, in.Agent, in.Conversation, in.Tool} {
		if strings.TrimSpace(value) == "" {
			return Approval{}, "", tools.ErrInvalid
		}
	}
	if !json.Valid(in.Arguments) {
		return Approval{}, "", tools.ErrInvalid
	}
	args, err := canonicalArguments(in.Arguments)
	if err != nil {
		return Approval{}, "", tools.ErrInvalid
	}
	// Copy only request facts. Lifecycle, scope and answer audit belong to Core.
	a := Approval{
		TaskID:    in.TaskID,
		EffectKey: in.EffectKey, Person: in.Person, Agent: in.Agent, Conversation: in.Conversation,
		Tool: in.Tool, Plugin: workerRelayPlugin, Arguments: args,
		What: in.What, Where: in.Where, Target: in.Target,
		Undoable: in.Undoable, Network: in.Network, Outward: in.Outward,
		Choices: []Choice{Once, Deny}, State: Pending,
	}
	// Epoch and generated identifiers are not identity: a retry after restart
	// must find the old request and be refused, never create a fresh approval.
	data, err := json.Marshal(a)
	return a, digest(data), err
}

// RequestWorkerApproval admits a request from Core's trusted worker adapter.
// Arguments must already be masked for display; raw parameters stay in the
// adapter. This API neither checks a model tool grant nor creates a standing one.
// Reusing an effect key with changed request facts returns tools.ErrKeyConflict.
func (s *Service) RequestWorkerApproval(ctx context.Context, in Approval) (Approval, error) {
	a, fingerprint, err := workerRequest(in)
	if err != nil {
		return Approval{}, err
	}
	target := a.Target
	if target == "" {
		target = a.Conversation
	}
	if err := CheckPreset(ctx, s.store, s.cfg.Epoch, PolicyRequest{Key: "preset:" + a.EffectKey, Person: a.Person, Agent: a.Agent, TaskID: a.TaskID, Tool: a.Tool, Target: target, Class: tools.WorkerEffect, Arguments: a.Arguments}); err != nil {
		return Approval{}, err
	}
	created := false
	err = s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		old, hash, err := readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE effect_key=?", a.EffectKey)
		if err == nil {
			if old.Plugin != workerRelayPlugin || hash != fingerprint {
				return tools.ErrKeyConflict
			}
			if old.Epoch != s.cfg.Epoch || old.State == Stopped || old.State == Consumed {
				return ErrStale
			}
			a = old
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if a.State == Pending && (s.cfg.CanPresent == nil || !s.cfg.CanPresent()) {
			return ErrNoSurface
		}
		if err == nil {
			return nil
		}
		a.ID = rand.Text()
		a.ScopeKey = workerRelayPlugin + ":" + a.ID
		a.Epoch = s.cfg.Epoch
		data, err := json.Marshal(a)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", a.ID, a.EffectKey, fingerprint, data)
		created = err == nil
		return err
	})
	if err != nil {
		return Approval{}, err
	}
	if created && s.cfg.ApprovalsChanged != nil {
		s.cfg.ApprovalsChanged(ctx)
	}
	return a, nil
}

func (s *Service) workerApproval(ctx context.Context, tx *canon.Tx, id string) (Approval, error) {
	if err := s.checkEpoch(ctx, tx); err != nil {
		return Approval{}, err
	}
	a, _, err := readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE approval_id=?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrStale
	}
	if err != nil {
		return Approval{}, err
	}
	if a.Plugin != workerRelayPlugin || a.Epoch != s.cfg.Epoch {
		return Approval{}, ErrStale
	}
	return a, nil
}

// ClaimWorkerAnswer atomically consumes one authenticated answer for relay to
// the worker. The returned record is Consumed and retains its Choice and audit.
// It deliberately does not notify ApprovalsChanged: that callback may be the
// caller draining answers after Answer commits.
func (s *Service) ClaimWorkerAnswer(ctx context.Context, id string) (Approval, error) {
	var a Approval
	err := s.store.Write(ctx, func(tx *canon.Tx) error {
		var err error
		a, err = s.workerApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if (a.State != Approved || a.Choice != Once) && (a.State != Denied || a.Choice != Deny) {
			return ErrStale
		}
		a.State = Consumed
		return saveApproval(ctx, tx, a)
	})
	if err != nil {
		return Approval{}, err
	}
	return a, nil
}

// StopWorkerApproval withdraws an unclaimed relay request when the worker
// resolves it or closes. Repeated stops are harmless; consumed answers are stale.
func (s *Service) StopWorkerApproval(ctx context.Context, id string) error {
	changed := false
	err := s.store.Write(ctx, func(tx *canon.Tx) error {
		a, err := s.workerApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		switch a.State {
		case Stopped:
			return nil
		case Pending, Approved, Denied:
			a.State = Stopped
		case Consumed:
			return ErrStale
		default:
			return ErrStale
		}
		if err := saveApproval(ctx, tx, a); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed && s.cfg.ApprovalsChanged != nil {
		s.cfg.ApprovalsChanged(ctx)
	}
	return err
}
