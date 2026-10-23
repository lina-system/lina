// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

var errDevUnavailable = errors.New("dev_agent_unavailable")

type codingAssignment struct {
	ID, Manager, Owner, Conversation string
	Origin                           dispatch.Origin
	Tool, Target                     string
	Arguments                        json.RawMessage
	Delegated                        bool
	Verdict                          string
}

type codingTurn struct {
	Assignment codingAssignment
	Phase      string
	Result     *dispatch.Record
}

func codingID(parts ...string) string {
	raw, _ := json.Marshal(parts)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func readCoding(ctx context.Context, tx *canon.Tx, id string) (codingAssignment, error) {
	var raw []byte
	var a codingAssignment
	err := tx.QueryRowContext(ctx, "SELECT record FROM coding_assignments WHERE id=?", id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &a)
	}
	return a, err
}

func saveCoding(ctx context.Context, tx *canon.Tx, a codingAssignment) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE coding_assignments SET record=? WHERE id=?", raw, a.ID)
	return err
}

// The origin supplies an already authorized coding request, not a new grant.
// Core chooses dev from immutable preset identity and keeps one manager per
// work unit. A renamed/deleted manager is never silently replaced.
func (r *conversationRuntime) assignCoding(ctx context.Context, origin dispatch.Origin, id, tool, target string, args json.RawMessage) (codingAssignment, error) {
	a := codingAssignment{ID: id, Manager: codingID(origin.Person, target), Origin: origin, Tool: tool, Target: target, Arguments: args}
	class := tools.WorkerEffect
	if tool == "prepare_work" {
		class = tools.LocalWrite
	}
	err := authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{Key: "preset:coding:" + id, Person: origin.Person, Agent: origin.Agent, TaskID: origin.Turn, Tool: tool, Target: target, Class: class, Arguments: args})
	if err != nil {
		return a, err
	}
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		prior, err := readCoding(ctx, tx, id)
		if err == nil {
			if prior.Origin != origin || prior.Tool != tool || prior.Target != target || string(prior.Arguments) != string(args) {
				return tools.ErrKeyConflict
			}
			a = prior
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT owner,conversation FROM coding_managers WHERE id=? AND person=?", a.Manager, origin.Person).Scan(&a.Owner, &a.Conversation)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT a.id FROM agents a JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=1 WHERE a.preset='dev' AND a.trashed_at=0 AND r.person_id=? ORDER BY a.id LIMIT 1", origin.Person).Scan(&a.Owner)
			if errors.Is(err, sql.ErrNoRows) {
				return errDevUnavailable
			}
			if err != nil {
				return err
			}
			a.Conversation = "manager:" + a.Manager
			if _, err = tx.ExecContext(ctx, "INSERT INTO coding_managers(id,person,owner,unit,conversation) VALUES(?,?,?,?,?)", a.Manager, origin.Person, a.Owner, target, a.Conversation); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var live int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id=? AND preset='dev' AND trashed_at=0", a.Owner).Scan(&live); err != nil {
			return err
		}
		if live != 1 {
			return errDevUnavailable
		}
		raw, _ := json.Marshal(a)
		_, err = tx.ExecContext(ctx, "INSERT INTO coding_assignments(id,manager,record) VALUES(?,?,?)", id, a.Manager, raw)
		return err
	})
	if err != nil {
		return a, err
	}
	if err = r.personaAuthority.BindDelegation(ctx, origin.Person, origin.Agent, origin.Turn, a.Owner, "coding:"+id); err != nil {
		return a, err
	}
	if err = r.personaAuthority.BindDelegation(ctx, origin.Person, origin.Agent, origin.Turn, a.Owner, id); err != nil {
		return a, err
	}
	return a, nil
}

// Manager turns use the existing prepared/journaled conversation engine, with
// their own durable ledger and the dev persona. They receive only the work
// definition and observed delivery, not the requesting persona's private history.
func (r *conversationRuntime) runCodingManager(ctx context.Context, a codingAssignment, phase string, result *dispatch.Record) error {
	if err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, a.ID); return err }); err != nil {
		return err
	}
	if phase == "assignment" && a.Delegated || phase == "delivery" && a.Verdict != "" {
		return nil
	}
	owner, err := r.agents.Get(ctx, a.Owner)
	if err != nil || owner.Preset != "dev" {
		return errDevUnavailable
	}
	view := newConversationView(r.router.root, a.Owner, a.Conversation)
	view.coding = &codingTurn{Assignment: a, Phase: phase, Result: result}
	id := "coding-" + phase + ":" + a.ID
	turnID := "turn:" + id
	if err = r.personaAuthority.BindDelegation(ctx, a.Origin.Person, a.Owner, "coding:"+a.ID, a.Owner, turnID); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"kind": "coding_" + phase, "origin_agent": a.Origin.Agent, "work_definition": json.RawMessage(a.Arguments), "delivery": result})
	text := "Core coding handoff. Interpret the work definition, scope and done condition as dev. For assignment, delegate only if the definition is sufficient; otherwise explain what is missing. For delivery, judge the observed artifacts and PR against that same definition. A worker report is a claim, not acceptance. Record your decision with coding_decision. Do not claim a merge or deployment.\n" + string(body)
	event, err := view.journal().Get(ctx, id)
	if errors.Is(err, ledger.ErrNotFound) {
		now := time.Now()
		event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: a.Conversation, AgentID: a.Owner, Surface: ledger.Surface{Kind: "manager"}, ScaffoldKind: "coding_handoff", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	}
	if err != nil {
		return err
	}
	row, err := inbox.New(r.db).Admit(ctx, inbox.Input{Key: inbox.Key{Conversation: a.Conversation, Source: "coding", NativeID: id}, Body: []byte(text)})
	if err != nil {
		return err
	}
	done, err := r.queueReaction(ctx, a.Conversation, func(ctx context.Context, inputs *loop.Inputs) error {
		if phase == "delivery" && result != nil && result.PR != nil {
			card, err := r.work.Get(ctx, a.ID)
			if err != nil {
				inputs.Close()
				return err
			}
			if card.Head != result.PR.Head {
				inputs.Close()
				return work.ErrState
			}
		}
		return view.run(ctx, inputs, event, row.ID)
	})
	if err != nil {
		return err
	}
	select {
	case err = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if err = r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, a.ID); return err }); err != nil {
		return err
	}
	if phase == "assignment" && !a.Delegated {
		return errors.New("coding_definition_not_delegated")
	}
	if phase == "delivery" && a.Verdict == "" {
		return errors.New("coding_delivery_unverified")
	}
	return nil
}

func (r *conversationRuntime) prepareCodingDecision(turnID string, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	c := r.coding
	spec := tools.Spec{Name: "coding_decision", Classify: tools.Class(tools.LocalWrite), Conc: 1, Deadline: time.Minute, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
		var a struct {
			Decision string `json:"decision"`
			Evidence string `json:"evidence"`
		}
		if decodeWorkerArgs(raw, &a) != nil || strings.TrimSpace(a.Evidence) == "" || len(a.Evidence) > 8192 {
			return tools.Input{}, tools.ErrInvalid
		}
		if c.Phase == "assignment" && a.Decision != "delegate" {
			return tools.Input{}, tools.ErrInvalid
		}
		if c.Phase == "delivery" && a.Decision != "verified" && a.Decision != "needs_changes" && a.Decision != "unverified" {
			return tools.Input{}, tools.ErrInvalid
		}
		if c.Phase == "delivery" && a.Decision == "verified" && (c.Result == nil || c.Result.PR == nil || c.Result.Code != "ready_for_review" || c.Result.ChangeEvidence == "" || c.Result.BaseCommit == "") {
			return tools.Input{}, tools.ErrInvalid
		}
		return tools.Input{Arguments: raw, Target: "coding:" + c.Assignment.ID + ":" + c.Phase, What: "Record dev manager decision", Where: c.Assignment.Conversation, ScopeKey: "coding-manager", Write: true, Undoable: true}, nil
	}}
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	if err := registry.Register(spec, func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		var decision struct {
			Decision string `json:"decision"`
			Evidence string `json:"evidence"`
		}
		_ = json.Unmarshal(in.Arguments, &decision)
		if c.Phase == "delivery" && decision.Decision != "unverified" {
			card, err := r.work.Get(ctx, c.Assignment.ID)
			if err != nil || card.Receipt == nil {
				return tools.Result{State: tools.Failed}, errors.New("coding_receipt_missing")
			}
			if err = r.work.Judge(ctx, card.Task.ID, card.Receipt.EventID, decision.Decision, decision.Evidence); err != nil {
				return tools.Result{State: tools.Failed}, err
			}
		}
		err := r.db.Write(ctx, func(tx *canon.Tx) error {
			a, err := readCoding(ctx, tx, c.Assignment.ID)
			if err != nil {
				return err
			}
			if a.Owner != r.agent {
				return tools.ErrInvalid
			}
			if c.Phase == "assignment" {
				a.Delegated = true
			} else {
				a.Verdict = decision.Decision
			}
			return saveCoding(ctx, tx, a)
		})
		if err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		raw, _ := json.Marshal(map[string]string{"owner": r.agent, "manager": c.Assignment.Conversation, "decision": decision.Decision})
		return tools.Result{State: tools.Applied, Revision: "recorded", Body: raw}, nil
	}); err != nil {
		return nil, err
	}
	auth := grant.Request{GrantID: turnID + ":coding", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "coding-manager", Revision: "unrecorded"}
	target := "coding:" + c.Assignment.ID + ":" + c.Phase
	r.personaMu.Lock()
	r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: target, Write: true}}, Revisions: map[string]string{target: auth.Revision}}
	r.personaMu.Unlock()
	bindings[spec.Name] = turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID)
	return []provider.ToolDecl{provider.ToolDecl(`{"type":"function","name":"coding_decision","description":"Record the owning dev manager's scoped assignment or delivery judgment. Use delegate only for a sufficient work definition; delivery verdicts are verified, needs_changes or unverified, with evidence. This does not merge or deploy.","parameters":{"type":"object","properties":{"decision":{"type":"string","enum":["delegate","verified","needs_changes","unverified"]},"evidence":{"type":"string"}},"required":["decision","evidence"],"additionalProperties":false}}`)}, nil
}
