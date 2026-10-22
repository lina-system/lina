// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrDefinitionMissing = errors.New("goal_definition_missing")

type DefinitionRecord struct {
	ID, Path string
	Data     []byte
}
type DefinitionChange struct {
	ID, Base, Path string
	Data           []byte
}

// Definitions is supplied by Core composition. Work owns execution only; the
// records family owns canonical Markdown, versioning and publication recovery.
type Definitions interface {
	Read(context.Context, string) (DefinitionRecord, string, error)
	Commit(context.Context, DefinitionChange, string, string) (string, error)
}
type Definition struct {
	Outcome     string         `json:"outcome"`
	Done        string         `json:"done"`
	Scope       GoalScope      `json:"scope"`
	Assumptions []string       `json:"assumptions,omitempty"`
	Decisions   []GoalDecision `json:"decisions,omitempty"`
	Criteria    []Criterion    `json:"criteria"`
}

// Definitions are immutable per record revision. In-flight journal transactions
// may still reference the prior revision while a refresh publishes a new one.
type definitionKey struct{ id, revision string }

type savedDefinition struct {
	definition     Definition
	revision, text string
}

func definition(g Goal) Definition {
	criteria := []Criterion{}
	for _, c := range g.Criteria {
		if !strings.HasPrefix(c.ID, "code:") {
			criteria = append(criteria, c)
		}
	}
	return Definition{g.Summary, g.Done, g.Scope, g.Assumptions, g.Decisions, criteria}
}
func applyDefinition(g *Goal, d Definition) {
	g.Summary = d.Outcome
	g.Done = d.Done
	g.Scope = d.Scope
	g.Assumptions = d.Assumptions
	g.Decisions = d.Decisions
	g.Criteria = d.Criteria
	g.Criteria = goalCriteria(*g)
}
func definitionBytes(g Goal) []byte {
	d := definition(g)
	return []byte(fmt.Sprintf("---\nid: %s\nkind: goal\nstate: active\nperson_id: %s\nagent_id: %s\ngoal: %s\n---\n\n# %s\n\n%s\n", encode(g.RecordID), encode(g.Person), encode(g.Agent), encode(d), g.Summary, g.Done))
}
func parseDefinition(r DefinitionRecord, person, agent string) (Definition, error) {
	var d Definition
	front, _, ok := bytes.Cut(r.Data, []byte("\n---\n"))
	if !ok {
		return d, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for _, line := range strings.Split(string(front), "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if ok {
			if _, duplicate := fields[key]; duplicate {
				return d, ErrInvalid
			}
			fields[key] = json.RawMessage(value)
		}
	}
	var p, a string
	if json.Unmarshal(fields["person_id"], &p) != nil || json.Unmarshal(fields["agent_id"], &a) != nil || p != person || a != agent || json.Unmarshal(fields["goal"], &d) != nil {
		return d, ErrInvalid
	}
	return d, nil
}
func (e *Engine) prepareDefinition(ctx context.Context, g *Goal) error {
	if e.cfg.Records == nil {
		return ErrInvalid
	}
	if g.RecordID != "" || g.RecordRevision != "" {
		return ErrInvalid
	}
	// Stable id makes an interrupted record publication recoverable on admission.
	g.RecordID = "goal:" + g.ID
	r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
	if errors.Is(err, ErrDefinitionMissing) {
		rev, err = e.cfg.Records.Commit(ctx, DefinitionChange{ID: g.RecordID, Path: "projects/" + actionID(Action{GoalID: g.ID}) + ".md", Data: definitionBytes(*g)}, g.Person, g.Agent)
		if err != nil {
			return err
		}
		r, rev, err = e.cfg.Records.Read(ctx, g.RecordID)
	}
	if err != nil {
		return err
	}
	d, err := parseDefinition(r, g.Person, g.Agent)
	if err != nil {
		return err
	}
	if !bytes.Equal(encode(d), encode(definition(*g))) {
		return ErrConflict
	}
	g.RecordRevision = rev
	e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
	return nil
}
func (e *Engine) hydrate(g *Goal) error {
	cached, ok := e.definitions.Load(definitionKey{g.RecordID, g.RecordRevision})
	if !ok {
		return ErrConflict
	}
	saved := cached.(savedDefinition)
	if saved.revision != g.RecordRevision {
		return ErrConflict
	}
	applyDefinition(g, saved.definition)
	g.DefinitionText = saved.text
	return nil
}

// RefreshDefinitions reads records outside any canon transaction. A changed
// definition fences execution until one manager turn interprets the correction.
func (e *Engine) RefreshDefinitions(ctx context.Context) error {
	rows, err := e.journal.Records(ctx, "goal")
	if err != nil {
		return err
	}
	for _, row := range rows {
		var g Goal
		if err = json.Unmarshal(row.Body, &g); err != nil {
			return err
		}
		r, rev, err := e.cfg.Records.Read(ctx, g.RecordID)
		if err != nil {
			return err
		}
		d, err := parseDefinition(r, g.Person, g.Agent)
		if err != nil {
			return err
		}
		e.definitions.Store(definitionKey{g.RecordID, rev}, savedDefinition{d, rev, string(r.Data)})
		if rev == g.RecordRevision {
			continue
		}
		err = e.journal.Update(ctx, Transition{ID: "goal.record:" + g.ID + ":" + rev, Kind: "goal.revised", At: e.cfg.Now().Unix()}, func(v *View) error {
			current, err := loadGoal(v, g.ID)
			if err != nil {
				return err
			}
			if current.RecordRevision == rev {
				return errNoChange
			}
			if current.RecordRevision != g.RecordRevision {
				return ErrConflict
			}
			if current.State == GoalStopped {
				return errNoChange
			}
			current.RecordRevision = rev
			current.Revision++
			current.DefinitionPending = true
			current.State = GoalRunning
			current.Checks = nil
			current.CheckRound++
			for _, r := range v.Records("action") {
				var a Action
				if err = json.Unmarshal(r.Body, &a); err != nil {
					return err
				}
				if a.GoalID == g.ID && a.State == "queued" {
					restoreUndispatchedTask(&current, a)
					a.State = "finished"
					if err = put(v, "action", a.ID, a); err != nil {
						return err
					}
				}
			}
			return put(v, "goal", g.ID, current)
		})
		if err != nil && !errors.Is(err, errNoChange) {
			return err
		}
	}
	return nil
}

// A correction may retire an undispatched reservation, but it must not leave
// the task waiting for a worker that never started or spend a recovery.
func restoreUndispatchedTask(g *Goal, a Action) {
	t := goalTask(g, a.TaskID)
	if t == nil {
		return
	}
	switch a.Kind {
	case Assign:
		t.Stage, t.NodeID = TaskReady, ""
	case Resume:
		t.Stage = TaskWorkerLost
		t.Recoveries = max(0, t.Recoveries-1)
	case RepairConflict:
		t.Stage = TaskConflicted
		t.Conflicts = max(0, t.Conflicts-1)
	case RepairQA:
		t.Stage = TaskQAFailed
		t.QARepairs = max(0, t.QARepairs-1)
	case ContinueWorker:
		t.Stage = TaskNoReceipt
		t.Continuations = max(0, t.Continuations-1)
	case PlanGoal:
		t.RepairAsked = false
	case InspectPR, Merge, RunQA, Deploy, Verify, Observe, Decide, CheckCriterion:
		return
	default:
		return
	}
	if reserves(a) {
		t.Attempt = max(0, t.Attempt-1)
		t.EffectsSettled = true
	}
}

// Execution history and evidence stay in the journal. Only the current task
// projection drops superseded work after its effects have settled. A landed
// dependency remains satisfied; an unlanded one requires replanning dependents.
func retainScopedTasks(repositories []string, tasks []Task) []Task {
	removed := map[string]Task{}
	for _, task := range tasks {
		if !has(repositories, task.Repository) {
			removed[task.ID] = task
		}
	}
	for changed := true; changed; {
		changed = false
		for _, task := range tasks {
			if _, ok := removed[task.ID]; ok {
				continue
			}
			for _, id := range task.DependsOn {
				if predecessor, ok := removed[id]; ok && !predecessor.Merge.proves(predecessor.Merge.Revision) {
					removed[task.ID] = task
					changed = true
					break
				}
			}
		}
	}
	kept := []Task{}
	for _, task := range tasks {
		if _, ok := removed[task.ID]; ok {
			continue
		}
		dependencies := []string{}
		for _, id := range task.DependsOn {
			if _, ok := removed[id]; !ok {
				dependencies = append(dependencies, id)
			}
		}
		task.DependsOn = dependencies
		kept = append(kept, task)
	}
	return kept
}
