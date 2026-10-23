// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type handoffRequest struct {
	To        string           `json:"to"`
	Handoff   exchange.HandOff `json:"handoff"`
	NextCheck time.Time        `json:"next_check"`
}
type changeRequest struct {
	Artifact   exchange.Artifact `json:"artifact"`
	Done       string            `json:"done"`
	Wanted     string            `json:"wanted"`
	OutOfScope string            `json:"out_of_scope"`
	NextCheck  time.Time         `json:"next_check"`
	Deadline   time.Time         `json:"deadline"`
}

func exchangeResult(id string, value any, err error) loop.Result {
	if err != nil {
		code, name := loop.Failed, "handoff_unavailable"
		if errors.Is(err, exchange.ErrInvalid) || errors.Is(err, exchange.ErrConflict) {
			code, name = loop.Invalid, "invalid_handoff"
		}
		raw, _ := json.Marshal(map[string]string{"code": name})
		return loop.Result{ID: id, Code: code, Output: raw}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return loop.Result{ID: id, Code: loop.Failed}
	}
	return loop.Result{ID: id, Code: loop.OK, Output: raw}
}
func (r *conversationRuntime) exchangeStore() *exchange.Store {
	return exchange.NewWithMaterials(r.db, r.materials)
}
func (r *conversationRuntime) exchangeAgent(ctx context.Context, id string) error {
	var n int
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM agents a JOIN agent_revisions v ON v.agent_id=a.id AND v.revision=1 WHERE a.id=? AND a.trashed_at=0 AND v.person_id=?`, id, r.person).Scan(&n)
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return exchange.ErrInvalid
	}
	return nil
}
func (r *conversationRuntime) handoffOrigin(ctx context.Context, turnID string, next time.Time) (*exchange.Origin, error) {
	p, err := r.agents.Prepare(ctx, turnID, r.agent)
	if err != nil {
		return nil, err
	}
	state, err := r.agents.State(ctx, r.agent)
	if err != nil {
		return nil, err
	}
	settings := agents.ProjectWorkMethod(p, state)
	check := true
	if err = json.Unmarshal(settings.Values["handoff.check_back"], &check); err != nil {
		return nil, err
	}
	return &exchange.Origin{Person: r.person, Conversation: r.conversation, CheckBack: check, NextCheck: next}, nil
}
func (r *conversationRuntime) artifactOwner(ctx context.Context, kind string) (string, error) {
	rows, err := r.agents.List(ctx)
	if err != nil {
		return "", err
	}
	var owner string
	for _, a := range rows {
		if err = r.exchangeAgent(ctx, a.ID); err != nil {
			continue
		}
		p, err := r.agents.Prepare(ctx, "artifact-owner:"+a.ID+":"+strconv.FormatInt(a.Revision, 10), a.ID)
		if err != nil {
			return "", err
		}
		state, err := r.agents.State(ctx, a.ID)
		if err != nil {
			return "", err
		}
		var kinds []string
		if err = json.Unmarshal(agents.ProjectWorkMethod(p, state).Values["handoff.owns"], &kinds); err != nil {
			return "", err
		}
		if slices.Contains(kinds, kind) {
			if owner != "" {
				return "", exchange.ErrConflict
			}
			owner = a.ID
		}
	}
	if owner == "" {
		return "", exchange.ErrInvalid
	}
	return owner, nil
}
func (r *conversationRuntime) prepareExchangeTools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	// Delayed authority is bound to the private owner lane, as timer jobs are.
	if r.channelBinding != nil || r.conversation != "main:"+r.agent {
		return nil, nil
	}
	store := r.exchangeStore()
	bindings["send_handoff"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		var a handoffRequest
		err := timerDecode(c.Arguments, &a)
		return "handoff:" + r.agent, err
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var a handoffRequest
		if timerDecode(c.Arguments, &a) != nil {
			return exchangeResult(c.ID, nil, exchange.ErrInvalid)
		}
		if err := r.exchangeAgent(ctx, a.To); err != nil {
			return exchangeResult(c.ID, nil, err)
		}
		origin, err := r.handoffOrigin(ctx, turnID, a.NextCheck)
		if err != nil {
			return exchangeResult(c.ID, nil, err)
		}
		receipt, err := store.Start(ctx, exchange.Request{DeliveryID: "handoff:" + codingID(turnID, c.ID), From: r.agent, To: a.To, HandOff: a.Handoff, Origin: origin}, time.Now())
		if err != nil {
			return exchangeResult(c.ID, nil, err)
		}
		status, err := r.handoffAdmission(ctx, receipt)
		return exchangeResult(c.ID, status, err)
	}}
	bindings["handoff_status"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) { var a timerRef; return "", timerDecode(c.Arguments, &a) }, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var a timerRef
		if timerDecode(c.Arguments, &a) != nil {
			return exchangeResult(c.ID, nil, exchange.ErrInvalid)
		}
		status, err := store.Status(ctx, a.ID, r.person, r.agent)
		return exchangeResult(c.ID, status, err)
	}}
	bindings["begin_artifact_change"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		var a changeRequest
		err := timerDecode(c.Arguments, &a)
		return "artifact:" + a.Artifact.ID, err
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var a changeRequest
		if timerDecode(c.Arguments, &a) != nil {
			return exchangeResult(c.ID, nil, exchange.ErrInvalid)
		}
		r.artifactMutex().Lock()
		defer r.artifactMutex().Unlock()
		owned, err := store.Change(ctx, r.person, a.Artifact.ID)
		owner := owned.Owner
		if errors.Is(err, sql.ErrNoRows) {
			owner, err = r.artifactOwner(ctx, a.Artifact.Kind)
		}
		if err == nil {
			err = r.exchangeAgent(ctx, owner)
		}
		if err != nil {
			return exchangeResult(c.ID, nil, err)
		}
		origin, err := r.handoffOrigin(ctx, turnID, a.NextCheck)
		if err != nil {
			return exchangeResult(c.ID, nil, err)
		}
		id := "change:" + codingID(turnID, c.ID)
		out, err := store.BeginChange(ctx, exchange.Change{Person: r.person, Artifact: a.Artifact, Owner: owner, WorkID: id, Done: a.Done}, exchange.Request{DeliveryID: id, From: r.agent, To: owner, Origin: origin, HandOff: exchange.HandOff{Subject: a.Done, Wanted: a.Wanted, OutOfScope: a.OutOfScope, Done: a.Done, Check: "artifact-result", Deadline: a.Deadline}}, time.Now())
		return exchangeResult(c.ID, out, err)
	}}
	bindings["finish_artifact_change"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		var a timerRef
		return "artifact:" + r.agent, timerDecode(c.Arguments, &a)
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var a timerRef
		if timerDecode(c.Arguments, &a) != nil {
			return exchangeResult(c.ID, nil, exchange.ErrInvalid)
		}
		r.artifactMutex().Lock()
		defer r.artifactMutex().Unlock()
		err := store.EndChange(ctx, r.person, r.agent, a.ID)
		return exchangeResult(c.ID, map[string]string{"state": "closed"}, err)
	}}
	return exchangeDeclarations(), nil
}

// All record write entry points call this while holding artifactMu, shared with
// lease admission/release. The second writer receives the durable hand-off and
// never reaches the original mutation. The owner retains the open work.
func (r *conversationRuntime) routeArtifactWrite(ctx context.Context, turnID string, c provider.ToolCall, id string) (*loop.Result, error) {
	owned, err := r.exchangeStore().Change(ctx, r.person, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owned.Owner == r.agent {
		return nil, nil
	}
	if err = r.exchangeAgent(ctx, owned.Owner); err != nil {
		return nil, err
	}
	origin, err := r.handoffOrigin(ctx, turnID, time.Now().Add(time.Hour))
	if err != nil {
		return nil, err
	}
	// A shared-channel write must not strand its check-back or grant delayed
	// channel posting authority. The sender's private lane owns this item.
	origin.Conversation = "main:" + r.agent
	origin.Artifact = owned.Artifact
	receipt, err := r.exchangeStore().Start(ctx, exchange.Request{DeliveryID: "handoff:" + codingID(turnID, c.Name, c.ID), From: r.agent, To: owned.Owner, Origin: origin, HandOff: exchange.HandOff{Subject: owned.Done, Wanted: c.Name + "\n" + c.Arguments, OutOfScope: "Only the named artifact; no external effects.", Done: owned.Done, Check: "artifact-result", Deadline: time.Now().Add(24 * time.Hour)}}, time.Now())
	if err != nil {
		return nil, err
	}
	status, err := r.handoffAdmission(ctx, receipt)
	result := exchangeResult(c.ID, map[string]any{"handoff": status, "changed": false}, err)
	return &result, nil
}

func (r *conversationRuntime) artifactMutex() *sync.Mutex {
	root := r
	if r.router != nil {
		root = r.router.root
	}
	root.artifactInit.Do(func() {
		if root.artifactMu == nil {
			root.artifactMu = &sync.Mutex{}
		}
	})
	return root.artifactMu
}

// A new tool receipt describes the queued admission at its commit, independent
// of scheduler speed. handoff_status reads the later delivery receipt.
func (r *conversationRuntime) handoffAdmission(ctx context.Context, receipt exchange.Receipt) (exchange.Status, error) {
	status, err := r.exchangeStore().Status(ctx, receipt.ID, r.person, r.agent)
	if err == nil && receipt.Inserted {
		status.Delivery = "pending"
		status.Outcome = exchange.Pending
		status.Report = nil
		if status.Waiting != "off" {
			status.Waiting = "waiting"
		}
	}
	return status, err
}
