// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func (r *conversationRuntime) mockOperation(ctx context.Context, session clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(session, nil); err != nil {
		return clientsrv.Result{}, err
	}
	p := e.Payload.(protocol.MockRequest)
	agent, ok := strings.CutPrefix(p.ConversationID, "main:")
	if !ok {
		if err := r.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT agent FROM result_heads WHERE id=? AND person=? AND conversation=?", p.ResultID, session.PersonID, p.ConversationID).Scan(&agent)
		}); err != nil {
			return clientsrv.Result{}, outsideManagement()
		}
		binding, err := r.channelBindingFor(ctx, p.ConversationID, agent)
		if err != nil || binding.Person != session.PersonID || binding.Unreachable {
			return clientsrv.Result{}, outsideManagement()
		}
	}
	if agent == "" {
		return clientsrv.Result{}, outsideManagement()
	}
	if _, err := r.agents.Get(ctx, agent); err != nil {
		return clientsrv.Result{}, outsideManagement()
	}
	rev, err := p.Revision.Int64()
	if err != nil || rev < 1 {
		return clientsrv.Result{}, malformedManagement()
	}
	store, err := r.resultStore(ledger.Surface{Kind: "app"})
	if err != nil {
		return clientsrv.Result{}, err
	}
	scope := result.Scope{PersonID: session.PersonID, AgentID: agent, ConversationID: p.ConversationID, TurnID: "mock-request:" + e.MessageID}
	value, err := store.ReadRevision(ctx, scope, p.ResultID, int(rev))
	if err != nil || value.Type != result.MockType {
		return clientsrv.Result{}, outsideManagement()
	}
	var body result.Mock
	if err = result.Decode(value.Body, &body); err != nil {
		return clientsrv.Result{}, err
	}
	answer := protocol.MockResponse{}
	switch p.Action {
	case "read":
		if p.Asset != nil || p.Offset != nil || p.Target != nil || p.Repositories != nil {
			return clientsrv.Result{}, malformedManagement()
		}
		wire := authoredResultWire(value, value.Summary)
		answer.Result = &wire
	case "asset":
		if p.Asset == nil || p.Target != nil || p.Repositories != nil {
			return clientsrv.Result{}, malformedManagement()
		}
		ref, media := body.Page, "application/zip"
		if *p.Asset == "png" {
			ref, media = body.PNG, "image/png"
		} else if *p.Asset != "page" {
			return clientsrv.Result{}, malformedManagement()
		}
		asset, err := r.assets.Get(ctx, ref.ID)
		if err != nil || asset.Actor.PersonID != scope.PersonID || asset.Actor.AgentID != scope.AgentID {
			return clientsrv.Result{}, outsideManagement()
		}
		data, err := r.assets.Read(ctx, ref.ID, int64(ref.Revision))
		if err != nil {
			return clientsrv.Result{}, err
		}
		var offset int64
		if p.Offset != nil {
			offset, err = p.Offset.Int64()
		}
		if err != nil || offset < 0 || offset > int64(len(data)) {
			return clientsrv.Result{}, malformedManagement()
		}
		end := min(offset+(48<<10), int64(len(data)))
		encoded := base64.StdEncoding.EncodeToString(data[offset:end])
		answer.Asset = &protocol.MockAssetRef{AssetID: ref.ID, Revision: json.Number(strconv.Itoa(ref.Revision))}
		answer.Data = &encoded
		answer.MediaType = &media
		if end < int64(len(data)) {
			next := json.Number(strconv.FormatInt(end, 10))
			answer.Next = &next
		}
	case "build":
		if p.Target == nil || p.Repositories == nil || p.Asset != nil || p.Offset != nil || e.IdempotencyKey == nil || *e.IdempotencyKey == "" {
			return clientsrv.Result{}, malformedManagement()
		}
		head, err := store.Read(ctx, scope, result.ReadRequest{ID: value.ID})
		if err != nil {
			return clientsrv.Result{}, err
		}
		if head.Revision != value.Revision {
			return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
		}
		engine := r.workEngine
		if engine == nil && r.router != nil {
			engine = r.router.root.workEngine
		}
		if engine == nil {
			return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
		}
		keyHash := sha256.Sum256([]byte(session.PersonID + "\x00" + *e.IdempotencyKey))
		scope.TurnID = "mock-build:" + hex.EncodeToString(keyHash[:])
		goal, err := mockGoal(scope, value, body, *p.Target, *p.Repositories, *e.IdempotencyKey)
		if err != nil {
			return clientsrv.Result{}, malformedManagement()
		}
		view, err := r.router.view(ctx, session, scope.AgentID)
		if err != nil {
			return clientsrv.Result{}, err
		}
		method, err := view.preparedMethod(ctx, scope.TurnID, scope.AgentID)
		if err != nil {
			return clientsrv.Result{}, err
		}
		if !dispatches(method) {
			args, _ := json.Marshal(goal)
			assigned, err := view.assignCoding(ctx, dispatch.Origin{Person: scope.PersonID, Agent: scope.AgentID, Conversation: scope.ConversationID, Turn: scope.TurnID}, goal.ID, "dispatch_code_goal", strings.Join(goal.Scope.Repositories, "\n"), args)
			if err != nil {
				return clientsrv.Result{}, err
			}
			goal.Agent, goal.Conversation, goal.Turn = assigned.Owner, assigned.Conversation, "coding:"+assigned.ID
		}
		if err = engine.engine.Admit(ctx, goal); err != nil && !errors.Is(err, work.ErrConflict) {
			return clientsrv.Result{}, err
		}
		if errors.Is(err, work.ErrConflict) {
			snapshot, readErr := engine.engine.Snapshot(ctx)
			if readErr != nil {
				return clientsrv.Result{}, readErr
			}
			matched := false
			for _, old := range snapshot.Goals {
				if old.ID == goal.ID && old.Person == goal.Person && old.Agent == goal.Agent && old.Summary == goal.Summary && strings.Join(old.Assumptions, "\n") == strings.Join(goal.Assumptions, "\n") && strings.Join(old.Scope.Repositories, "\n") == strings.Join(goal.Scope.Repositories, "\n") {
					matched = true
				}
			}
			if !matched {
				return clientsrv.Result{}, malformedManagement()
			}
		}
		answer.GoalID = &goal.ID
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	return clientsrv.Result{Kind: protocol.KindMockResponse, Payload: answer}, nil
}
func mockGoal(scope result.Scope, value result.Result, body result.Mock, target string, repositories []string, key string) (work.Goal, error) {
	if target != "screen" && target != "mini_app" || len(repositories) == 0 || len(repositories) > 16 {
		return work.Goal{}, result.ErrMalformed
	}
	seen := map[string]bool{}
	for _, repo := range repositories {
		if strings.TrimSpace(repo) == "" || seen[repo] {
			return work.Goal{}, result.ErrMalformed
		}
		seen[repo] = true
	}
	pin, _ := json.Marshal(struct {
		Reference result.ResultRef `json:"reference"`
		Target    string           `json:"target"`
		Mock      result.Mock      `json:"mock"`
	}{result.ResultRef{ID: value.ID, Revision: value.Revision}, target, body})
	digest := sha256.Sum256([]byte(scope.PersonID + "\x00" + scope.AgentID + "\x00" + key))
	criteria := []work.Criterion{}
	for _, item := range body.Checklist {
		criteria = append(criteria, work.Criterion{ID: "mock:" + item.ID, Statement: item.Description, Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "Verify the implemented control and visible element against mock checklist item " + item.ID, Target: repositories[0]}})
	}
	return work.Goal{ID: "mock-build:" + hex.EncodeToString(digest[:]), Person: scope.PersonID, Agent: scope.AgentID, Conversation: scope.ConversationID, Turn: scope.TurnID, Summary: "Build " + target + ": " + body.Request, Done: "The implemented UI satisfies every pinned mock checklist item.", Scope: work.GoalScope{Repositories: repositories, Includes: body.Request, Excludes: "Changes outside the pinned mock and checklist."}, Assumptions: []string{"Pinned mock reference: " + string(pin)}, Criteria: criteria, NoServiceReason: "UI implementation; runtime and deployment requirements are determined by the work plan."}, nil
}
