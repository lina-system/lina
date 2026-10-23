// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	return d.resolveInputs(ctx, a, false)
}

func (d *codeGoalDriver) resolveInputs(ctx context.Context, a goal.Action, observe bool) (goal.State, goal.Goal, goal.Task, codeRepository, error) {
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
		if observe && goal.WorkerAction(a.Kind) {
			// GoalRecord verifies the original conversation/action binding.
			// Its retained receipt needs neither a current repository grant
			// nor an installed connection to observe a completed worker.
			return s, g, task, codeRepository{}, nil
		}
		name := task.Repository
		if a.Check != nil {
			if connection, ok := d.connections.EvidenceChecks[a.Check.Method]; ok {
				if connection.Kind != a.Check.Kind || connection.Target != a.Check.Target || a.Check.TaskID != "" || connection.Kind == goal.ReadEvidence && len(a.Check.Expected) == 0 {
					break
				}
				name = connection.Repository
			}
		}
		if name == "" && a.Check != nil && a.Check.Method != "deploy.read" && a.Check.Method != "runtime.probe" {
			name = a.Check.Target
		}
		if name == "" && g.ServiceRepository != "" {
			name = g.ServiceRepository
		}
		if name == "" && len(g.Scope.Repositories) > 0 {
			name = g.Scope.Repositories[0]
		}
		repo, ok := d.connections.Repositories[name]
		if !ok || !observe && !slices.Contains(g.Scope.Repositories, name) || (task.ID != "" && (task.Kind == "" || task.Kind == goal.TaskStep) && (task.Base != repo.Base || task.Worker != "antigravity")) {
			break
		}
		if observe {
			// Corrections retain tasks until their effects settle. Deployment
			// observation additionally verifies its original connection intent.
			return s, g, task, repo, nil
		}
		serviceRepository := g.ServiceRepository
		if serviceRepository == "" && len(g.Scope.Repositories) == 1 {
			serviceRepository = g.Scope.Repositories[0]
		}
		if g.ServiceTarget != "" && name == serviceRepository && (repo.Service == nil || repo.Service.Target != g.ServiceTarget) {
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
	if g.State == goal.GoalStopped || g.State == goal.GoalDone && a.Kind != goal.CheckCriterion || a.Epoch != d.runtime.epoch || a.GoalRevision != g.Revision {
		return goal.ErrNeedsPerson
	}
	if (a.Kind == goal.InspectPR || a.Kind == goal.Merge) && a.Revision != t.Head || a.Kind == goal.RunQA && a.Revision != t.Merge.Revision || (a.Kind == goal.Deploy || a.Kind == goal.Verify) && a.Revision != codeGoalRevision(g) {
		return goal.ErrNeedsPerson
	}
	if a.InputVersion == nil || a.InputVersion.GoalRevision != g.Revision || a.InputVersion.RecordRevision != g.RecordRevision {
		return goal.ErrNeedsPerson
	}
	for _, task := range g.Tasks {
		if a.TaskID == "" || a.TaskID == task.ID {
			if a.InputVersion.PRGenerations[task.ID] != task.PRGeneration {
				return goal.ErrNeedsPerson
			}
		}
	}
	class := tools.LocalWrite
	switch {
	case t.Kind == goal.WaitStep && goal.WorkerAction(a.Kind):
		class = tools.Research
	case t.Kind == goal.BackgroundStep && goal.WorkerAction(a.Kind):
		class = tools.WorkerEffect
		if d.runtime.background == nil || a.NodeID != d.runtime.main {
			return goal.ErrNeedsPerson
		}
	case goal.WorkerAction(a.Kind):
		class = tools.WorkerEffect
		if d.runtime.workerDispatch == nil || a.NodeID != d.runtime.main {
			return goal.ErrNeedsPerson
		}
	case a.Kind == goal.Merge:
		class = tools.BindingExecution
	case a.Kind == goal.Deploy:
		if repo.Service == nil {
			return goal.ErrNeedsPerson
		}
		if repo.Service.Public {
			class = tools.ExternalEffect
		}
	case a.Kind == goal.Observe || a.Kind == goal.Decide || a.Kind == goal.PlanGoal:
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
	if a.Kind == goal.Assign {
		if t.Kind == goal.BackgroundStep {
			return d.backgroundStep(ctx, g, t, a, false)
		}
		if t.Kind == goal.WaitStep {
			return d.waitStep(ctx, g, t, a)
		}
	}
	switch a.Kind {
	case goal.Assign:
		definition := dispatch.Definition{Worker: t.Worker, Repository: repo.Clone, Base: repo.Base, Branch: "lina/goal-" + codingID(g.ID, t.ID, a.ID)[:24], What: t.Done, Why: g.Summary, Scope: []string{g.Scope.Includes}, NotTouch: []string{g.Scope.Excludes}, Done: t.Done, DesignRefs: repo.DesignRefs, WriteArea: strings.Split(t.Area, ",")}
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
	s, g, t, repo, err := d.resolveInputs(ctx, a, true)
	if err != nil {
		return goal.Observation{}, err
	}
	switch {
	case a.Kind == goal.Assign && t.Kind == goal.BackgroundStep:
		return d.backgroundStep(ctx, g, t, a, true)
	case a.Kind == goal.Assign && t.Kind == goal.WaitStep:
		return d.waitStep(ctx, g, t, a)
	case goal.WorkerAction(a.Kind):
		return d.runtime.observeGoalWorker(ctx, g, a)
	case a.Kind == goal.Merge:
		record, err := d.runtime.goalTaskReceipt(ctx, s, g, t)
		if err != nil {
			return goal.Observation{}, err
		}
		return d.runtime.mergeGoalPR(ctx, record, t.Repository, a, true)
	case a.Kind == goal.RunQA:
		return d.readCheck(a, repo.QA, t.Repository)
	case a.Kind == goal.Deploy:
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
		if effect.Tool != "deploy_code_goal" || effect.Plugin != "" || effect.Account != "" || !effect.Write || effect.Outward != repo.Service.Public || effect.Person != g.Person || effect.Agent != g.Agent || effect.TaskID != g.Turn || effect.Target != "code-deploy:"+repo.Service.Target || effect.Revision != a.Revision {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		observation, err := d.runCheck(ctx, g, a, repo, repo.Service.Probe, repo.Service.Target, false)
		if err != nil || observation.Status != goal.OutcomeSucceeded || !observation.Proof.Passed {
			// An unhealthy service does not establish the deployment's outcome.
			// Retain its evidence and reservation for a later read-only probe.
			observation.Status = goal.OutcomeUnknown
			observation.EffectsSettled = false
			return observation, err
		}
		body, _ := json.Marshal(map[string]any{"source": "probe", "proof": observation.Proof})
		if err = d.runtime.personaAuthority.ReconcileApplied(ctx, effect, tools.Result{State: tools.Applied, Revision: a.Revision, Body: body}); err != nil {
			return goal.Observation{}, err
		}
		return observation, nil
	case a.Kind == goal.InspectPR || a.Kind == goal.Verify || a.Kind == goal.CheckCriterion:
		// Recovered reads use the new Core epoch without redispatching an effect.
		a.Epoch = d.runtime.epoch
		return d.Execute(ctx, a)
	case a.Kind == goal.Observe || a.Kind == goal.Decide || a.Kind == goal.PlanGoal:
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	return goal.Observation{}, goal.ErrInvalid
}
func (d *codeGoalDriver) libraryProof(ctx context.Context, s goal.State, g goal.Goal, a goal.Action) (goal.Observation, error) {
	var current goal.Proof
	onlyResults := len(g.Tasks) > 0
	for _, t := range g.Tasks {
		if t.Kind == goal.BackgroundStep || t.Kind == goal.WaitStep {
			if _, err := d.readStepResult(ctx, s, g, t); err != nil {
				return goal.Observation{}, err
			}
			continue
		}
		onlyResults = false
		repo, ok := d.connections.Repositories[t.Repository]
		if !ok || !t.QA.Passed {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		check, err := d.readProof(t.QA.Ref, repo.QA, t.Repository, t.QA.Revision)
		if err != nil || !check.Passed {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		if check.Revision == a.Revision {
			current = check
		}
	}
	if onlyResults {
		current = goal.Proof{Revision: a.Revision, Ref: "steps:" + g.ID, Passed: a.Revision != ""}
	}
	if current.Ref == "" {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	return goal.Observation{Status: goal.OutcomeSucceeded, Proof: current}, nil
}
func (d *codeGoalDriver) checkCriterion(ctx context.Context, s goal.State, g goal.Goal, t goal.Task, a goal.Action, repo codeRepository) (goal.Observation, error) {
	if a.Check == nil {
		return goal.Observation{}, goal.ErrInvalid
	}
	var o goal.Observation
	var err error
	var collected *goal.CollectedEvidence
	switch a.Check.Method {
	case "steps.read":
		if a.Check.Kind != goal.ReadEvidence || a.Check.TaskID != "" {
			return o, goal.ErrInvalid
		}
		o, err = d.libraryProof(ctx, s, g, a)
	case "step.read":
		if a.Check.Kind != goal.ReadEvidence || a.Check.TaskID == "" || a.Check.Target != t.ID {
			return o, goal.ErrInvalid
		}
		o.Proof, err = d.readStepResult(ctx, s, g, t)
	case "merge.read":
		record, e := d.runtime.goalTaskReceipt(ctx, s, g, t)
		if e != nil {
			return o, e
		}
		merge, e := d.runtime.workerGitHub.ReadLanded(ctx, ghclient.Ref{Repository: t.Repository, Number: record.PR.Number}, t.Base, t.Head, a.Revision)
		if e != nil {
			return o, e
		}
		o = goal.Observation{Status: goal.OutcomeSucceeded, Proof: goal.Proof{Revision: merge.Commit, Ref: record.PR.URL, Passed: merge.State == "merged" && merge.Commit == a.Revision && merge.Head == t.Head}}
		raw, _ := json.Marshal(merge)
		collected = &goal.CollectedEvidence{Record: raw}
	case "qa.read":
		o.Proof, err = d.readProof(t.QA.Ref, repo.QA, t.Repository, a.Revision)
	case "deploy.read", "runtime.probe":
		if repo.Service != nil {
			o, err = d.runCheck(ctx, g, a, repo, repo.Service.Probe, repo.Service.Target, false)
		} else {
			o, err = d.libraryProof(ctx, s, g, a)
		}
	default:
		if connection, ok := d.connections.EvidenceChecks[a.Check.Method]; ok {
			if connection.Target != a.Check.Target || connection.Kind != a.Check.Kind || !slices.Contains(g.Scope.Repositories, connection.Repository) || connection.Kind == goal.ReadEvidence && len(a.Check.Expected) == 0 {
				return o, goal.ErrNeedsPerson
			}
			o, err = d.runCheck(ctx, g, a, repo, connection.Command, a.Check.Target, false)
		} else {
			command, ok := repo.Checks[a.Check.Method]
			if !ok || a.Check.Kind != goal.RunEvidence || !slices.Contains(g.Scope.Repositories, a.Check.Target) {
				return o, goal.ErrNeedsPerson
			}
			o, err = d.runCheck(ctx, g, a, repo, command, a.Check.Target, true)
		}
	}
	if err != nil {
		return o, err
	}
	if o.Proof.Ref == "" || o.Proof.Revision != a.Revision {
		return o, fmt.Errorf("code goal evidence revision mismatch")
	}
	if collected == nil {
		collected, err = d.collectedCheck(o.Proof.Ref, *a.Check)
		if err != nil {
			return o, err
		}
	}
	if !collected.Valid(*a.Check) {
		return o, goal.ErrInvalid
	}
	o.Status = goal.OutcomeSucceeded
	o.Criterion = &goal.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: o.Proof.Ref, Passed: o.Proof.Passed && collected.Matches(*a.Check), Collected: collected}
	return o, nil
}

func codeGoalRevision(g goal.Goal) string {
	repository := g.ServiceRepository
	if repository == "" && g.ServiceTarget != "" && len(g.Scope.Repositories) == 1 {
		repository = g.Scope.Repositories[0]
	}
	if revision := g.MergedRevisions[repository]; revision != "" {
		return revision
	}
	return g.TargetRevision
}
