// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

var (
	ErrNoSurface = errors.New("approval_surface_unavailable")
	ErrStale     = errors.New("stale_approval")
	ErrDecider   = errors.New("invalid_approval_decider")
	ErrChoice    = errors.New("invalid_approval_choice")
	ErrDenied    = errors.New("approval_denied")
)

type Choice string

const (
	Once   Choice = "once"
	Always Choice = "always"
	Deny   Choice = "deny"
)

type ApprovalState string

const (
	Pending  ApprovalState = "pending"
	Approved ApprovalState = "approved"
	Denied   ApprovalState = "denied"
	Stopped  ApprovalState = "stopped"
	Consumed ApprovalState = "consumed"
)

type Surface string

const (
	App     Surface = "app"
	Channel Surface = "channel"
	Text    Surface = "text"
)

// Decider contains facts established by the authenticated connection boundary,
// not fields decoded from the button or from model text. Channel Person is the
// paired owner established by that adapter. A button carries only ID and choice.
type Decider struct {
	Person                        string
	Epoch                         uint64
	Surface                       Surface
	Authenticated, CoreDescendant bool
	ChatID, MessageID             string
}
type AgentScope struct {
	All bool
	IDs []string
}
type Decision struct {
	Choice Choice
	Agents *AgentScope
}
type Approval struct {
	ID, EffectKey, What, Where, ScopeKey, Target, Plugin, Tool string
	Arguments                                                  json.RawMessage
	Undoable, Network, Outward                                 bool
	Choices                                                    []Choice
	Epoch                                                      uint64
	Person, Agent                                              string
	State                                                      ApprovalState
	Choice                                                     Choice
	AnsweredBy, ChatID, MessageID                              string
	AnsweredAt                                                 time.Time
	StandingID                                                 string
}
type StandingGrant struct {
	ID, Person, ScopeKey, Target string
	AllAgents                    bool
	Agents                       []string
	CreatedBy, RequestingAgent   string
	CreatedAt                    time.Time
	Revoked                      bool
	RevokedBy                    string
	RevokedAt                    time.Time
}

func (s *Service) validateDecider(d Decider) error {
	if d.Epoch != s.cfg.Epoch {
		return ErrStale
	}
	if !d.Authenticated || d.CoreDescendant || d.Person == "" {
		return ErrDecider
	}
	switch d.Surface {
	case App:
		return nil
	case Channel:
		if d.ChatID != "" && d.MessageID != "" {
			return nil
		}
	case Text:
		return ErrDecider
	}
	return ErrDecider
}
func readApproval(ctx context.Context, tx *canon.Tx, query string, arg string) (Approval, string, error) {
	var data []byte
	var hash string
	err := tx.QueryRowContext(ctx, query, arg).Scan(&hash, &data)
	var a Approval
	if err == nil {
		err = json.Unmarshal(data, &a)
	}
	return a, hash, err
}
func saveApproval(ctx context.Context, tx *canon.Tx, a Approval) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE authority_approvals SET record=? WHERE approval_id=?", data, a.ID)
	return err
}
func allApprovals(ctx context.Context, tx *canon.Tx) ([]Approval, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM authority_approvals ORDER BY approval_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var a Approval
		if err := json.Unmarshal(data, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Service) recoverApprovals(ctx context.Context, tx *canon.Tx) error {
	approvals, err := allApprovals(ctx, tx)
	if err != nil {
		return err
	}
	for _, a := range approvals {
		if a.State == Pending || a.State == Approved {
			a.State = Stopped
			if err := saveApproval(ctx, tx, a); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) Pending(ctx context.Context) ([]Approval, error) {
	var out []Approval
	err := s.store.Read(ctx, func(tx *canon.Tx) error {
		all, err := allApprovals(ctx, tx)
		if err != nil {
			return err
		}
		for _, a := range all {
			if a.State == Pending {
				out = append(out, a)
			}
		}
		return nil
	})
	return out, err
}
func (s *Service) Approval(ctx context.Context, id string) (Approval, error) {
	var a Approval
	err := s.store.Read(ctx, func(tx *canon.Tx) error {
		var err error
		a, _, err = readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE approval_id=?", id)
		return err
	})
	return a, err
}
func standing(ctx context.Context, tx *canon.Tx, person string) ([]StandingGrant, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM authority_standing WHERE person=? ORDER BY grant_id", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StandingGrant{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var g StandingGrant
		if err := json.Unmarshal(data, &g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
func (s *Service) Standing(ctx context.Context, person string) ([]StandingGrant, error) {
	var out []StandingGrant
	err := s.store.Read(ctx, func(tx *canon.Tx) error { var err error; out, err = standing(ctx, tx, person); return err })
	return out, err
}

// approvalBasis runs in the same transaction as the effect claim. In particular,
// once answers are consumed and standing revocations are observed atomically.
// ErrApprovalRequired may have inserted a request, so the caller must commit it.
func (s *Service) approvalBasis(ctx context.Context, tx *canon.Tx, c tools.Call, spec tools.Spec, in tools.Input, fingerprint string) (string, error) {
	network := in.Network && spec.Plugin == "" && !spec.Outward
	if !spec.Outward && !network {
		return "grant", nil
	}
	a, hash, err := readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE effect_key=?", c.Key)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if exists {
		if hash != fingerprint {
			return "", tools.ErrKeyConflict
		}
		if a.Epoch != s.cfg.Epoch || a.State == Stopped || a.State == Consumed {
			return "", ErrStale
		}
		if a.State == Denied {
			return "", ErrDenied
		}
	}
	if network {
		grants, err := standing(ctx, tx, c.Auth.Person)
		if err != nil {
			return "", err
		}
		for _, g := range grants {
			if !g.Revoked && g.ScopeKey == in.ScopeKey && g.Target == in.Target && (g.AllAgents || slices.Contains(g.Agents, c.Auth.Agent)) {
				if exists {
					a.State = Consumed
					if err := saveApproval(ctx, tx, a); err != nil {
						return "", err
					}
				}
				return "standing:" + g.ID, nil
			}
		}
	}
	if exists {
		if a.State == Approved {
			// Always authorizes only through its still-live standing grant above.
			if a.Choice != Once {
				return "", ErrStale
			}
			a.State = Consumed
			if err := saveApproval(ctx, tx, a); err != nil {
				return "", err
			}
			return "approval:" + a.ID, nil
		}
		if s.cfg.CanPresent == nil || !s.cfg.CanPresent() {
			return "", ErrNoSurface
		}
		return "", tools.ErrApprovalRequired
	}
	if s.cfg.CanPresent == nil || !s.cfg.CanPresent() {
		return "", ErrNoSurface
	}
	choices := []Choice{Once, Deny}
	if network {
		choices = []Choice{Once, Always, Deny}
	}
	a = Approval{ID: rand.Text(), EffectKey: c.Key, What: in.What, Where: in.Where, ScopeKey: in.ScopeKey, Target: in.Target, Plugin: spec.Plugin, Tool: spec.Name, Arguments: append(json.RawMessage(nil), in.Arguments...), Undoable: in.Undoable, Network: network, Outward: spec.Outward, Choices: choices, Epoch: s.cfg.Epoch, Person: c.Auth.Person, Agent: c.Auth.Agent, State: Pending}
	data, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", a.ID, c.Key, fingerprint, data); err != nil {
		return "", err
	}
	return "", tools.ErrApprovalRequired
}
func (s *Service) Answer(ctx context.Context, id string, d Decider, decision Decision) error {
	if err := s.validateDecider(d); err != nil {
		return err
	}
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		a, _, err := readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE approval_id=?", id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStale
		}
		if err != nil {
			return err
		}
		if a.State != Pending || a.Epoch != s.cfg.Epoch {
			return ErrStale
		}
		if a.Person != d.Person {
			return ErrDecider
		}
		if !slices.Contains(a.Choices, decision.Choice) || decision.Choice == Always && d.Surface != App {
			return ErrChoice
		}
		if decision.Agents != nil && decision.Choice != Always {
			return ErrChoice
		}
		a.State = Approved
		a.Choice = decision.Choice
		a.AnsweredBy = d.Person
		a.ChatID = d.ChatID
		a.MessageID = d.MessageID
		a.AnsweredAt = time.Now().UTC()
		switch decision.Choice {
		case Deny:
			a.State = Denied
		case Once:
		case Always:
			if !a.Network || a.Plugin != "" || a.Outward {
				return ErrChoice
			}
			scope := AgentScope{All: true}
			if decision.Agents != nil {
				scope = *decision.Agents
			}
			if scope.All && len(scope.IDs) > 0 {
				return ErrChoice
			}
			for _, id := range scope.IDs {
				if id == "" {
					return ErrChoice
				}
			}
			g := StandingGrant{ID: rand.Text(), Person: a.Person, ScopeKey: a.ScopeKey, Target: a.Target, AllAgents: scope.All, Agents: slices.Clone(scope.IDs), CreatedBy: d.Person, RequestingAgent: a.Agent, CreatedAt: a.AnsweredAt}
			data, err := json.Marshal(g)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO authority_standing(grant_id,person,record) VALUES(?,?,?)", g.ID, g.Person, data); err != nil {
				return err
			}
			a.StandingID = g.ID
		default:
			return ErrChoice
		}
		return saveApproval(ctx, tx, a)
	})
}
func (s *Service) RevokeStanding(ctx context.Context, id string, d Decider) error {
	if err := s.validateDecider(d); err != nil {
		return err
	}
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		var data []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM authority_standing WHERE grant_id=?", id).Scan(&data); err != nil {
			return err
		}
		var g StandingGrant
		if err := json.Unmarshal(data, &g); err != nil {
			return err
		}
		if g.Person != d.Person {
			return ErrDecider
		}
		if g.Revoked {
			return nil
		}
		g.Revoked = true
		g.RevokedBy = d.Person
		g.RevokedAt = time.Now().UTC()
		data, err := json.Marshal(g)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE authority_standing SET record=? WHERE grant_id=?", data, id)
		return err
	})
}
