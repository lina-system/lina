// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func goalOrigin(g goal.Goal) dispatch.Origin {
	return dispatch.Origin{Person: g.Person, Agent: g.Agent, Conversation: g.Conversation, Turn: g.Turn}
}

func goalWorkerObservation(r dispatch.Record) goal.Observation {
	o := goal.Observation{Handle: r.ID, Code: r.Code, Status: goal.OutcomeUnknown}
	if r.State == "queued" || r.State == "running" {
		o.Status = goal.OutcomeRunning
	} else if r.State == "finished" {
		o.EffectsSettled = true
		o.Status = goal.OutcomeNeedsPerson
		if r.Code == "ready_for_review" && r.PR != nil {
			o.Status, o.Head = goal.OutcomeSucceeded, r.PR.Head
		}
	}
	return o
}

// A goal dispatch uses the same worker, work receipt and dev manager as a
// conversation dispatch. An engine observation never calls RunGoal.
func (r *conversationRuntime) runGoalWorker(ctx context.Context, g goal.Goal, a goal.Action, definition dispatch.Definition) (goal.Observation, error) {
	if r.workerDispatch == nil || g.Conversation == "" || g.Turn == "" {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	record, err := r.workerDispatch.RunGoal(ctx, goalOrigin(g), a.ID, definition)
	if err != nil {
		return goal.Observation{}, err
	}
	return r.goalDeliveryObservation(ctx, record)
}

func (r *conversationRuntime) observeGoalWorker(ctx context.Context, g goal.Goal, a goal.Action) (goal.Observation, error) {
	if r.workerDispatch == nil {
		return goal.Observation{Status: goal.OutcomeUnknown}, nil
	}
	record, err := r.workerDispatch.GoalRecord(ctx, goalOrigin(g), a.ID)
	if err != nil {
		return goal.Observation{}, err
	}
	return r.goalDeliveryObservation(ctx, record)
}

// Keep the original goal worker action open while the single definition
// correction runs. Only its final receipt/head crosses the goal input fence.
func (r *conversationRuntime) goalDeliveryObservation(ctx context.Context, record dispatch.Record) (goal.Observation, error) {
	observation := goalWorkerObservation(record)
	if record.State != "finished" || record.PR == nil || record.Owner == "" {
		return observation, nil
	}
	var a codingAssignment
	if err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
		return goal.Observation{}, err
	}
	state, err := r.deliverCoding(ctx, a, record)
	if err != nil {
		return goal.Observation{}, err
	}
	switch state {
	case "returned":
		return goal.Observation{Status: goal.OutcomeRunning, Handle: record.ID, Code: "definition_returned"}, nil
	case "verified":
		return observation, nil
	default:
		return goal.Observation{Status: goal.OutcomeNeedsPerson, Handle: record.ID, Code: state, EffectsSettled: true}, nil
	}
}

// Only a receipt accepted through the input fence can supply the task's current
// attempt. Unchanged tasks may retain that acceptance across record revisions;
// corrected tasks retire it through Superseded. Neither worker prose nor the
// model supplies a PR number, protected baseline or manager verdict.
func (r *conversationRuntime) goalTaskReceipt(ctx context.Context, s goal.State, g goal.Goal, t goal.Task) (dispatch.Record, error) {
	if r.workerDispatch == nil {
		return dispatch.Record{}, goal.ErrNeedsPerson
	}
	for _, a := range s.Actions {
		if a.GoalID != g.ID || a.TaskID != t.ID || a.Attempt != t.Attempt || a.Superseded || a.State != "finished" || a.InputVersion == nil {
			continue
		}
		if goal.WorkerAction(a.Kind) {
			var receipt goal.Observation
			if json.Unmarshal(a.Receipt, &receipt) != nil || receipt.Status != goal.OutcomeSucceeded || !receipt.EffectsSettled || receipt.Head != t.Head {
				continue
			}
			source := a.ID
			if a.ObserveActionID != "" {
				source = a.ObserveActionID
			}
			record, err := r.workerDispatch.GoalRecord(ctx, goalOrigin(g), source)
			if err != nil {
				return record, err
			}
			if record.State != "finished" || record.Code != "ready_for_review" || record.PR == nil || record.PR.Head != t.Head {
				return record, goal.ErrNeedsPerson
			}
			return record, nil
		}
	}
	return dispatch.Record{}, goal.ErrNeedsPerson
}

func (r *conversationRuntime) inspectGoalPR(ctx context.Context, record dispatch.Record, repository string) (goal.Observation, error) {
	if r.workerGitHub == nil || record.PR == nil {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	p, err := antigravity.ReadPullRequest(ctx, r.workerGitHub, repository, record.TaskDefinition().Branch, record.Definition.Base, record.PR.Head)
	if err != nil {
		return goal.Observation{}, err
	}
	refs, err := r.workerGitHub.ProtectedRefs(ctx, record.Definition.Repository, repository, slices.Sorted(maps.Keys(record.Protected)))
	if err != nil || !maps.Equal(refs, record.Protected) {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	var assignment codingAssignment
	if err = r.db.Read(ctx, func(tx *canon.Tx) error { var err error; assignment, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
		return goal.Observation{}, err
	}
	record.PR = &p
	if !r.conformanceVerified(ctx, assignment, record) {
		if err = r.runCodingManager(ctx, assignment, "delivery", &record); err != nil {
			return goal.Observation{}, err
		}
	}
	if err = r.db.Read(ctx, func(tx *canon.Tx) error { var err error; assignment, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
		return goal.Observation{}, err
	}
	if !r.conformanceVerified(ctx, assignment, record) {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	proof := goal.Proof{Revision: p.Head, Ref: p.URL, Passed: true}
	return goal.Observation{Status: goal.OutcomeSucceeded, PR: goal.PRState{Head: p.Head, Checks: proof, Review: proof, Verified: goal.Proof{Revision: p.Head, Ref: "coding:" + record.ID, Passed: true}, Mergeable: true, ObservedAt: time.Now().Unix()}}, nil
}

func (r *conversationRuntime) mergeGoalPR(ctx context.Context, record dispatch.Record, repository string, a goal.Action, observe bool) (goal.Observation, error) {
	if r.workerGitHub == nil || record.PR == nil || a.Revision != record.PR.Head {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	merges := authority.NewMerges(r.db, r.epoch)
	ref := ghclient.Ref{Repository: repository, Number: record.PR.Number}
	binding := authority.MergeBinding{ID: record.ID, Person: record.Origin.Person, Agent: record.Owner, Repository: repository, Number: ref.Number, Base: record.Definition.Base, Clone: record.Definition.Repository, Protected: record.Protected}
	var result ghclient.MergeResult
	var err error
	// An interrupted write must be observed before an OPEN-only preflight.
	// GitHub may already have merged the pinned head while application is still
	// absent from our ledger. A new binding still follows the fresh preflight.
	current, err := merges.Get(ctx, record.ID, record.Origin.Person)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return goal.Observation{}, err
	}
	if current.State == "unknown" || current.State == "queued" || current.State == "merged" {
		if current.Head != "" && current.Head != a.Revision {
			return goal.Observation{}, authority.ErrMergeState
		}
		observe = true
	}
	if observe {
		// Another actor may have merged before our preflight created a row.
		// Recover bookkeeping from the retained dispatch, without Accept/Begin
		// or a merge command. Bind also refuses a conflicting existing row.
		if err = merges.Bind(ctx, binding); err != nil {
			return goal.Observation{}, err
		}
		result, err = r.workerGitHub.ReadLanded(ctx, ref, record.Definition.Base, a.Revision, "")
	} else {
		// Re-read the full current-head review and manager binding at dispatch.
		if _, err = r.inspectGoalPR(ctx, record, repository); err != nil {
			return goal.Observation{}, err
		}
		if err = merges.Bind(ctx, binding); err != nil {
			return goal.Observation{}, err
		}
		current, err = merges.Get(ctx, record.ID, record.Origin.Person)
		if err != nil {
			return goal.Observation{}, err
		}
		if current.State == "unknown" || current.State == "queued" || current.State == "merged" {
			return r.mergeGoalPR(ctx, record, repository, a, true)
		}
		if err = merges.Accept(ctx, record.ID, record.Origin.Person, a.Revision); err != nil {
			return goal.Observation{}, err
		}
		result, err = r.workerGitHub.Merge(ctx, ref, binding.Base, a.Revision, func(ctx context.Context) error {
			refs, err := r.workerGitHub.ProtectedRefs(ctx, binding.Clone, repository, slices.Sorted(maps.Keys(binding.Protected)))
			if err != nil {
				return err
			}
			return merges.Begin(ctx, record.ID, record.Origin.Person, a.Revision, refs)
		})
	}
	if result.State == "merged" || result.State == "unknown" || result.State == "queued" || result.State == "blocked" {
		raw, _ := json.Marshal(result)
		if recordErr := merges.Record(ctx, record.ID, record.Origin.Person, result.State, raw); recordErr != nil {
			return goal.Observation{}, errors.Join(err, recordErr)
		}
	}
	if err != nil {
		return goal.Observation{}, err
	}
	if result.State != "merged" || result.Head != a.Revision {
		return goal.Observation{Status: goal.OutcomeUnknown, Handle: record.ID, Code: result.Cause}, nil
	}
	proof := goal.Proof{Revision: result.Commit, Ref: record.PR.URL, Passed: true}
	card, err := r.work.Get(ctx, record.ID)
	if err != nil || card.Receipt == nil {
		return goal.Observation{}, goal.ErrInvalid
	}
	if err = r.work.Apply(ctx, record.ID, card.Receipt.EventID, record.PR.URL+"@"+result.Commit); err != nil {
		return goal.Observation{}, err
	}
	return goal.Observation{Status: goal.OutcomeSucceeded, Head: a.Revision, Proof: proof, EffectsSettled: true}, nil
}
