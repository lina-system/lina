// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/tools"
)

type codeGoalDriver struct {
	runtime     *conversationRuntime
	engine      *goal.Engine
	connections codeConnections
}

func (d *codeGoalDriver) inputs(ctx context.Context, a goal.Action) (goal.State, goal.Goal, goal.Task, codeRepository, error) {
	s, err := d.engine.Snapshot(ctx)
	if err != nil {
		return s, goal.Goal{}, goal.Task{}, codeRepository{}, err
	}
	for _, g := range s.Goals {
		if g.ID != a.GoalID {
			continue
		}
		if g.Person != d.runtime.person || g.Conversation == "" || g.Turn == "" {
			break
		}
		if _, err := d.runtime.agents.Get(ctx, g.Agent); err != nil {
			break
		}
		var task goal.Task
		taskID := a.TaskID
		if a.Kind == goal.CheckCriterion && a.Check != nil {
			taskID = a.Check.TaskID
		}
		for _, t := range g.Tasks {
			if t.ID == taskID {
				task = t
				break
			}
		}
		name := task.Repository
		if name == "" && len(g.Scope.Repositories) == 1 {
			name = g.Scope.Repositories[0]
		}
		repo, ok := d.connections.Repositories[name]
		if !ok || (task.ID != "" && (task.Base != repo.Base || task.Worker != "antigravity")) {
			break
		}
		if g.ServiceTarget != "" && (repo.Service == nil || repo.Service.Target != g.ServiceTarget) {
			break
		}
		// A repository with a service cannot suppress deploy by claiming no service.
		if g.ServiceTarget == "" && repo.Service != nil {
			break
		}
		return s, g, task, repo, nil
	}
	return s, goal.Goal{}, goal.Task{}, codeRepository{}, goal.ErrNeedsPerson
}

func (d *codeGoalDriver) Authorize(ctx context.Context, a goal.Action) error {
	_, g, t, repo, err := d.inputs(ctx, a)
	if err != nil {
		return err
	}
	if g.State == goal.GoalStopped || g.State == goal.GoalDone || a.Epoch != d.runtime.epoch || a.InputVersion == nil || a.InputVersion.GoalRevision != g.Revision || a.InputVersion.RecordRevision != g.RecordRevision {
		return goal.ErrNeedsPerson
	}
	if t.ID != "" && a.InputVersion.PRGenerations[t.ID] != t.PRGeneration {
		return goal.ErrNeedsPerson
	}
	if (a.Kind == goal.InspectPR || a.Kind == goal.Merge) && a.Revision != t.Head || a.Kind == goal.RunQA && a.Revision != t.Merge.Revision || (a.Kind == goal.Deploy || a.Kind == goal.Verify) && a.Revision != g.TargetRevision {
		return goal.ErrNeedsPerson
	}
	class := tools.LocalWrite
	switch a.Kind {
	case goal.Assign, goal.Resume, goal.RepairConflict, goal.RepairQA, goal.ContinueWorker:
		class = tools.WorkerEffect
		if d.runtime.workerDispatch == nil || a.NodeID != d.runtime.main {
			return goal.ErrNeedsPerson
		}
	case goal.Merge:
		class = tools.BindingExecution
	case goal.Deploy:
		if repo.Service == nil {
			return goal.ErrNeedsPerson
		}
		if repo.Service.Public {
			class = tools.ExternalEffect
		}
	case goal.InspectPR, goal.RunQA, goal.Verify, goal.CheckCriterion:
	case goal.Observe, goal.Decide, goal.PlanGoal:
		return goal.ErrNeedsPerson
	}
	raw, _ := json.Marshal(a)
	if err := authority.CheckPreset(ctx, d.runtime.db, d.runtime.epoch, authority.PolicyRequest{Key: "goal-policy:" + a.ID, Person: g.Person, Agent: g.Agent, TaskID: g.Turn, Tool: "code_goal_" + string(a.Kind), Target: repo.Clone, Class: class, Arguments: raw}); err != nil {
		return goal.ErrNeedsPerson
	}
	return nil
}
func (d *codeGoalDriver) Execute(ctx context.Context, a goal.Action) (goal.Observation, error) {
	if err := d.Authorize(ctx, a); err != nil {
		return goal.Observation{}, err
	}
	s, g, t, repo, err := d.inputs(ctx, a)
	if err != nil {
		return goal.Observation{}, err
	}
	switch a.Kind {
	case goal.Assign:
		definition := dispatch.Definition{Repository: repo.Clone, Base: repo.Base, Branch: "lina/goal-" + codingID(g.ID, t.ID, a.ID)[:24], What: t.Done, Why: g.Summary, Scope: []string{g.Scope.Includes}, NotTouch: []string{g.Scope.Excludes}, Done: t.Done, DesignRefs: repo.DesignRefs, WriteArea: strings.Split(t.Area, ",")}
		return d.runtime.runGoalWorker(ctx, g, a, definition)
	case goal.Resume, goal.RepairConflict, goal.RepairQA, goal.ContinueWorker:
		// The installed Antigravity adapter has no resume capability. Keep its
		// existing worktree and receipt, and ask instead of starting duplicate work.
		return goal.Observation{}, goal.ErrNeedsPerson
	case goal.InspectPR, goal.Merge:
		record, err := d.runtime.goalTaskReceipt(ctx, s, g, t)
		if err != nil {
			return goal.Observation{}, err
		}
		if a.Kind == goal.InspectPR {
			return d.runtime.inspectGoalPR(ctx, record, t.Repository)
		}
		return d.runtime.mergeGoalPR(ctx, record, t.Repository, a, false)
	case goal.RunQA:
		return d.runCheck(ctx, g, a, repo, repo.QA, t.Repository, true)
	case goal.Deploy:
		return d.deploy(ctx, g, a, repo)
	case goal.Verify:
		if repo.Service != nil {
			return d.runCheck(ctx, g, a, repo, repo.Service.Probe, repo.Service.Target, false)
		}
		return d.libraryProof(ctx, s, g, a)
	case goal.CheckCriterion:
		return d.checkCriterion(ctx, s, g, t, a, repo)
	case goal.Observe, goal.Decide, goal.PlanGoal:
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	return goal.Observation{}, goal.ErrInvalid
}
func (d *codeGoalDriver) Observe(ctx context.Context, a goal.Action) (goal.Observation, error) {
	s, g, t, repo, err := d.inputs(ctx, a)
	if err != nil {
		return goal.Observation{}, err
	}
	switch a.Kind {
	case goal.Assign, goal.Resume, goal.RepairConflict, goal.RepairQA, goal.ContinueWorker:
		return d.runtime.observeGoalWorker(ctx, g, a)
	case goal.Merge:
		record, err := d.runtime.goalTaskReceipt(ctx, s, g, t)
		if err != nil {
			return goal.Observation{}, err
		}
		return d.runtime.mergeGoalPR(ctx, record, t.Repository, a, true)
	case goal.RunQA:
		return d.readCheck(a, repo.QA, t.Repository)
	case goal.Deploy:
		// An interrupted deploy is settled only by the installed read-only probe.
		if repo.Service == nil {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		if err := d.deploymentIntent(a, repo, false); err != nil {
			return goal.Observation{Status: goal.OutcomeUnknown}, nil
		}
		effect, err := d.runtime.personaAuthority.Effect(ctx, a.ID)
		if err != nil || (effect.State != tools.Unknown && effect.State != tools.Applied) {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		return d.runCheck(ctx, g, a, repo, repo.Service.Probe, repo.Service.Target, false)
	case goal.InspectPR, goal.Verify, goal.CheckCriterion:
		// Recovered reads use the new Core epoch without redispatching an effect.
		a.Epoch = d.runtime.epoch
		return d.Execute(ctx, a)
	case goal.Observe, goal.Decide, goal.PlanGoal:
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	return goal.Observation{}, goal.ErrInvalid
}
func (d *codeGoalDriver) libraryProof(ctx context.Context, s goal.State, g goal.Goal, a goal.Action) (goal.Observation, error) {
	for _, t := range g.Tasks {
		repo, ok := d.connections.Repositories[t.Repository]
		if !ok || !t.QA.Passed {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		check, err := d.readProof(t.QA.Ref, repo.QA, t.Repository, t.QA.Revision)
		if err != nil || !check.Passed {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
	}
	if len(g.Tasks) == 0 {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	return goal.Observation{Status: goal.OutcomeSucceeded, Proof: goal.Proof{Revision: a.Revision, Ref: g.Tasks[len(g.Tasks)-1].QA.Ref, Passed: true}}, nil
}
func (d *codeGoalDriver) checkCriterion(ctx context.Context, s goal.State, g goal.Goal, t goal.Task, a goal.Action, repo codeRepository) (goal.Observation, error) {
	if a.Check == nil {
		return goal.Observation{}, goal.ErrInvalid
	}
	var o goal.Observation
	var err error
	switch a.Check.Method {
	case "merge.read":
		record, e := d.runtime.goalTaskReceipt(ctx, s, g, t)
		if e != nil {
			return o, e
		}
		merge, e := d.runtime.workerGitHub.ObserveMerge(ctx, ghclient.Ref{Repository: t.Repository, Number: record.PR.Number}, t.Base)
		if e != nil {
			return o, e
		}
		o = goal.Observation{Status: goal.OutcomeSucceeded, Proof: goal.Proof{Revision: merge.Commit, Ref: record.PR.URL, Passed: merge.State == "merged" && merge.Commit == a.Revision && merge.Head == t.Head}}
	case "qa.read":
		o.Proof, err = d.readProof(t.QA.Ref, repo.QA, t.Repository, a.Revision)
	case "deploy.read", "runtime.probe":
		if repo.Service != nil {
			o, err = d.runCheck(ctx, g, a, repo, repo.Service.Probe, repo.Service.Target, false)
		} else {
			o, err = d.libraryProof(ctx, s, g, a)
		}
	default:
		command, ok := repo.Checks[a.Check.Method]
		if !ok || a.Check.Kind != goal.RunEvidence || a.Check.Target != g.Scope.Repositories[0] {
			return o, goal.ErrNeedsPerson
		}
		o, err = d.runCheck(ctx, g, a, repo, command, a.Check.Target, true)
	}
	if err != nil {
		return o, err
	}
	if o.Proof.Ref == "" || o.Proof.Revision != a.Revision {
		return o, fmt.Errorf("code goal evidence revision mismatch")
	}
	o.Status = goal.OutcomeSucceeded
	o.Criterion = &goal.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: o.Proof.Ref, Passed: o.Proof.Passed}
	return o, nil
}
