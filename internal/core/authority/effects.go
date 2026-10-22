// SPDX-License-Identifier: Apache-2.0

// Package authority owns Core's approvals and durable effect admission.
package authority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

// Resolve reads the current grant, revocation and host permission state from
// Core's trusted owner. It must not manufacture a grant from the tool request.
type Resolve func(context.Context, string) (grant.Grant, grant.Environment, error)
type Config struct {
	// ApprovalsChanged observes committed approval changes, outside canon's write
	// transaction. It updates display projections only; failures cannot replay effects.
	ApprovalsChanged func(context.Context)
	Epoch            uint64
	Resolve          Resolve
	// CanPresent reports whether a connected surface can show the whole request.
	// A nil callback means no surface. It must only inspect local connection state.
	CanPresent func() bool
}
type Service struct {
	store *canon.Store
	cfg   Config
}
type Effect struct {
	Class                                            tools.EffectClass `json:",omitempty"`
	Key, Plugin, Account, Tool, ArgumentHash, Target string
	Person, Agent, TaskID, GrantID, Basis            string
	Epoch                                            uint64
	Outward, Write                                   bool
	Revision, CurrentRevision, PreviousAgent         string
	State                                            tools.State
	Result                                           tools.Result
}

func Open(ctx context.Context, store *canon.Store, cfg Config) (*Service, error) {
	if store == nil || cfg.Epoch == 0 || cfg.Resolve == nil {
		return nil, tools.ErrInvalid
	}
	s := &Service{store: store, cfg: cfg}
	// An interrupted external call cannot be inferred to have failed. Preserve
	// its target reservation until reconciliation; never automatically replay it.
	err := store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT effect_key,record FROM authority_effects WHERE running=1")
		if err != nil {
			return err
		}
		var records []Effect
		for rows.Next() {
			var key string
			var data []byte
			if err := rows.Scan(&key, &data); err != nil {
				rows.Close()
				return err
			}
			var e Effect
			if err = json.Unmarshal(data, &e); err != nil {
				rows.Close()
				return err
			}
			records = append(records, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range records {
			e.State = tools.Unknown
			e.Result = tools.Result{State: tools.Unknown}
			if err := saveEffect(ctx, tx, e, false); err != nil {
				return err
			}
		}
		return s.recoverApprovals(ctx, tx)
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Service) checkEpoch(ctx context.Context, tx *canon.Tx) error {
	var epoch string
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return err
	}
	if epoch != fmt.Sprint(s.cfg.Epoch) {
		return grant.ErrEpoch
	}
	return nil
}
func canonicalArguments(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func describe(c tools.Call, spec tools.Spec, in tools.Input) (Effect, string, error) {
	args, err := canonicalArguments(in.Arguments)
	if err != nil {
		return Effect{}, "", err
	}
	e := Effect{Key: c.Key, Plugin: spec.Plugin, Account: c.Account, Tool: spec.Name, ArgumentHash: digest(args), Target: in.Target, Person: c.Auth.Person, Agent: c.Auth.Agent, TaskID: c.Auth.TaskID, GrantID: c.Auth.GrantID, Epoch: c.Auth.Epoch, Outward: spec.Outward, Write: in.Write, Revision: c.Auth.Revision, Basis: "grant", State: tools.None}
	// Epoch is intentionally not part of effect identity: an identical retry after
	// restart must find the original receipt. Changed arguments or actors cannot.
	identity := e
	identity.Epoch = 0
	data, err := json.Marshal(struct {
		Effect                Effect
		What, Where, ScopeKey string
		Undoable, Network     bool
	}{identity, in.What, in.Where, in.ScopeKey, in.Undoable, in.Network})
	return e, digest(data), err
}
func readEffect(ctx context.Context, tx *canon.Tx, key string) (Effect, string, bool, error) {
	var data []byte
	var fingerprint string
	var running bool
	err := tx.QueryRowContext(ctx, "SELECT fingerprint,record,running FROM authority_effects WHERE effect_key=?", key).Scan(&fingerprint, &data, &running)
	var e Effect
	if err == nil {
		err = json.Unmarshal(data, &e)
	}
	return e, fingerprint, running, err
}
func saveEffect(ctx context.Context, tx *canon.Tx, e Effect, running bool) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE authority_effects SET record=?,running=? WHERE effect_key=?", data, running, e.Key)
	return err
}
func insertEffect(ctx context.Context, tx *canon.Tx, e Effect, fingerprint string, running bool) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO authority_effects(effect_key,fingerprint,record,running) VALUES(?,?,?,?)", e.Key, fingerprint, data, running)
	return err
}
func (s *Service) Effect(ctx context.Context, key string) (Effect, error) {
	var e Effect
	err := s.store.Read(ctx, func(tx *canon.Tx) error { var err error; e, _, _, err = readEffect(ctx, tx, key); return err })
	return e, err
}
func (s *Service) Begin(ctx context.Context, c tools.Call, spec tools.Spec, in tools.Input) (tools.Ticket, error) {
	if c.Key == "" || c.Name != spec.Name || c.Auth.Epoch != s.cfg.Epoch {
		return tools.Ticket{}, tools.ErrInvalid
	}
	class := spec.EffectClass(in)
	if class == tools.ExternalEffect || class == tools.InvestmentOrder || class == tools.BindingExecution {
		spec.Outward = true
	}
	c.Auth.Tool = spec.Name
	c.Auth.Target = in.Target
	c.Auth.Write = in.Write
	g, env, err := s.cfg.Resolve(ctx, c.Auth.GrantID)
	if err != nil {
		return tools.Ticket{}, err
	}
	authorization := grant.Check(g, c.Auth, env)
	if authorization != nil && !errors.Is(authorization, grant.ErrStaleRevision) {
		return tools.Ticket{}, authorization
	}
	e, fingerprint, err := describe(c, spec, in)
	if err != nil {
		return tools.Ticket{}, err
	}
	ticket := tools.Ticket{Key: c.Key}
	var denied error
	err = s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		e.Class = spec.EffectClass(in)
		limits, err := taskLimits(ctx, tx, c.Auth.Person, c.Auth.Agent, c.Auth.TaskID)
		if err != nil {
			return err
		}
		existing, oldHash, running, err := readEffect(ctx, tx, c.Key)
		if err == nil {
			if oldHash != fingerprint {
				return tools.ErrKeyConflict
			}
			if running {
				return tools.ErrInProgress
			}
			if existing.State == tools.Unknown {
				return tools.ErrUnknown
			}
			ticket.Replay = true
			ticket.Result = existing.Result
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !limits.Allows(e.Class) {
			denied = ErrPresetLimit
			e.State, e.Basis = tools.Refused, ErrPresetLimit.Error()
			e.Result = refusal(limits, e.Class)
			ticket.Result = e.Result
			return insertEffect(ctx, tx, e, fingerprint, false)
		}
		var active string
		if in.Write {
			err := tx.QueryRowContext(ctx, "SELECT revision,agent,active_key FROM authority_targets WHERE target=?", in.Target).Scan(&e.CurrentRevision, &e.PreviousAgent, &active)
			if errors.Is(err, sql.ErrNoRows) {
				e.CurrentRevision = env.Revision
			} else if err != nil {
				return err
			} else if active == "" && e.CurrentRevision != env.Revision {
				// A receipt describes Core's last write, not subsequent external
				// edits. Refresh idle targets from the executor's observation and
				// clear attribution: the previous agent did not produce this revision.
				e.CurrentRevision = env.Revision
				e.PreviousAgent = ""
				if _, err := tx.ExecContext(ctx, "UPDATE authority_targets SET revision=?,agent='' WHERE target=? AND active_key=''", env.Revision, in.Target); err != nil {
					return err
				}
			}
		}
		if authorization != nil {
			denied = authorization
			e.State = tools.Refused
			e.Result.State = tools.Refused
			if e.CurrentRevision != env.Revision {
				e.PreviousAgent = ""
			}
			e.CurrentRevision = env.Revision
			return insertEffect(ctx, tx, e, fingerprint, false)
		}
		if in.Write {
			if e.CurrentRevision != c.Auth.Revision {
				denied = grant.ErrStaleRevision
				e.State = tools.Refused
				e.Result.State = tools.Refused
				return insertEffect(ctx, tx, e, fingerprint, false)
			}
			if active != "" {
				return tools.ErrInProgress
			}

		}
		basis, err := s.approvalBasis(ctx, tx, c, spec, in, fingerprint)
		if errors.Is(err, tools.ErrApprovalRequired) {
			denied = err
			return nil
		}
		if errors.Is(err, ErrDenied) || errors.Is(err, ErrNoSurface) {
			denied = err
			e.State = tools.Refused
			e.Result.State = tools.Refused
			e.Basis = err.Error()
			return insertEffect(ctx, tx, e, fingerprint, false)
		}
		if err != nil {
			return err
		}
		e.Basis = basis
		if in.Write {
			if _, err := tx.ExecContext(ctx, "INSERT INTO authority_targets(target,revision,agent,active_key) VALUES(?,?,?,?) ON CONFLICT(target) DO UPDATE SET active_key=excluded.active_key", in.Target, e.CurrentRevision, e.PreviousAgent, c.Key); err != nil {
				return err
			}
		}
		return insertEffect(ctx, tx, e, fingerprint, true)
	})
	if err != nil {
		return tools.Ticket{}, err
	}
	if errors.Is(denied, tools.ErrApprovalRequired) && s.cfg.ApprovalsChanged != nil {
		s.cfg.ApprovalsChanged(ctx)
	}
	return ticket, denied
}
func (s *Service) Finish(ctx context.Context, ticket tools.Ticket, result tools.Result) error {
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		e, _, running, err := readEffect(ctx, tx, ticket.Key)
		if err != nil {
			return err
		}
		if !running {
			return tools.ErrKeyConflict
		}
		switch result.State {
		case tools.Applied, tools.Failed, tools.Cancelled, tools.Unknown:
		case tools.None, tools.Refused:
			return tools.ErrInvalid
		default:
			return tools.ErrInvalid
		}
		if result.State == tools.Applied && e.Write && result.Revision == "" {
			return tools.ErrInvalid
		}
		e.Result = result
		e.State = result.State
		if e.Write && result.State != tools.Unknown {
			if result.State == tools.Applied {
				_, err = tx.ExecContext(ctx, "UPDATE authority_targets SET revision=?,agent=?,active_key='' WHERE target=? AND active_key=?", result.Revision, e.Agent, e.Target, e.Key)
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE authority_targets SET active_key='' WHERE target=? AND active_key=?", e.Target, e.Key)
			}
			if err != nil {
				return err
			}
		}
		return saveEffect(ctx, tx, e, false)
	})
}
