// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

// Step contains manager-owned inputs only. Execution state remains in Task.
type Step struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Done       string    `json:"done"`
	Repository string    `json:"repository"`
	Base       string    `json:"base"`
	Area       string    `json:"area"`
	Worker     string    `json:"worker"`
	Resources  Resources `json:"resources"`
	DependsOn  []string  `json:"depends_on"`
}

type StepGraph struct {
	GoalID   string `json:"goal_id"`
	Revision uint64 `json:"revision"`
	Steps    []Step `json:"steps"`
}

type GraphError string

func (e GraphError) Error() string { return string(e) }

const (
	GraphInvalid       GraphError = "graph_invalid"
	GraphCycle         GraphError = "graph_cycle"
	GraphUnknownStep   GraphError = "graph_unknown_step"
	GraphInvalidArea   GraphError = "graph_invalid_area"
	GraphInvalidName   GraphError = "graph_invalid_name"
	GraphStartedStep   GraphError = "graph_started_step"
	GraphStaleRevision GraphError = "graph_stale_revision"
	GraphUnavailable   GraphError = "graph_unavailable"
)

func (s Step) task() Task {
	return Task{ID: s.ID, Name: s.Name, Done: s.Done, Repository: s.Repository, Base: s.Base, Area: s.Area, Worker: s.Worker, Resources: s.Resources, DependsOn: s.DependsOn, Stage: TaskReady}
}

func validateSteps(g Goal, steps []Step) error {
	if len(steps) == 0 || len(steps) > 256 {
		return GraphInvalid
	}
	ids := map[string]Step{}
	areaSets := map[string][]string{}
	forbidden := []string{g.ID, g.RecordID, "foundation"}
	for _, c := range g.Criteria {
		forbidden = append(forbidden, c.ID, c.Check.Method)
	}
	for _, s := range steps {
		if strings.TrimSpace(s.ID) == "" || len(s.ID) > 128 || strings.ContainsAny(s.ID, "\x00\r\n") {
			return GraphInvalid
		}
		if _, ok := ids[s.ID]; ok {
			return GraphUnknownStep
		}
		ids[s.ID] = s
		forbidden = append(forbidden, s.ID, s.Base)
	}
	for _, s := range steps {
		if !validStepName(s.Name, forbidden) {
			return GraphInvalidName
		}
		if len(s.DependsOn) > 256 {
			return GraphInvalid
		}
		seen := map[string]bool{}
		for _, dep := range s.DependsOn {
			if _, ok := ids[dep]; !ok || seen[dep] {
				return GraphUnknownStep
			}
			seen[dep] = true
		}
		areas := strings.Split(s.Area, ",")
		for i := range areas {
			areas[i] = strings.TrimSpace(areas[i])
		}
		if len(areas) > 64 {
			return GraphInvalidArea
		}
		areaSets[s.ID] = areas
		if _, err := work.ScheduledAreasOverlap(areas, areas); err != nil {
			return GraphInvalidArea
		}
	}
	color := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if color[id] == 1 {
			return false
		}
		if color[id] == 2 {
			return true
		}
		color[id] = 1
		for _, dep := range ids[id].DependsOn {
			if !visit(dep) {
				return false
			}
		}
		color[id] = 2
		return true
	}
	for _, s := range steps {
		if !visit(s.ID) {
			return GraphCycle
		}
	}
	// Reuse the scheduler's intersection grammar; overlapping steps must be ordered.
	reach := map[[2]string]bool{}
	checked := map[[2]string]bool{}
	var depends func(string, string) bool
	depends = func(id, ancestor string) bool {
		pair := [2]string{id, ancestor}
		if checked[pair] {
			return reach[pair]
		}
		checked[pair] = true
		for _, dep := range ids[id].DependsOn {
			if dep == ancestor || depends(dep, ancestor) {
				reach[pair] = true
				return true
			}
		}
		return false
	}
	for i, a := range steps {
		for _, b := range steps[i+1:] {
			if a.Repository != b.Repository || depends(a.ID, b.ID) || depends(b.ID, a.ID) {
				continue
			}
			overlap, err := work.ScheduledAreasOverlap(areaSets[a.ID], areaSets[b.ID])
			if err != nil || overlap {
				return GraphInvalidArea
			}
		}
	}
	proposed := g
	proposed.Tasks = nil
	for _, s := range steps {
		proposed.Tasks = append(proposed.Tasks, s.task())
	}
	proposed.Criteria = goalCriteria(proposed)
	if validateGoal(proposed) != nil {
		return GraphInvalid
	}
	return nil
}

func graphFor(g Goal) StepGraph {
	if g.StepGraph != nil {
		return *g.StepGraph
	}
	return StepGraph{GoalID: g.ID, Steps: []Step{}}
}
func graphOwner(g Goal, person, agent, conversation string) bool {
	return person != "" && agent != "" && g.Person == person && g.Agent == agent && g.Conversation == conversation
}

func (e *Engine) ReadStepGraph(ctx context.Context, id, person, agent, conversation string) (StepGraph, error) {
	var graph StepGraph
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		v, err := readView(ctx, tx)
		if err != nil {
			return err
		}
		g, err := loadGoal(v, id)
		if err != nil || !graphOwner(g, person, agent, conversation) {
			return GraphUnavailable
		}
		graph = graphFor(g)
		return nil
	})
	return graph, err
}

// SubmitStepGraph commits the whole revision and executable tasks together. No
// external effect runs in this transaction. The journal wakes the usual runner.
func (e *Engine) SubmitStepGraph(ctx context.Context, id, person, agent, conversation string, expected uint64, steps []Step) (StepGraph, error) {
	var result StepGraph
	// Copy caller-owned slices before they become part of a durable receipt.
	var owned []Step
	raw := encode(steps)
	if len(raw) > 256<<10 || json.Unmarshal(raw, &owned) != nil {
		return result, GraphInvalid
	}
	steps = owned
	ev := Transition{ID: "step-graph:" + rand.Text(), Kind: "goal.step_graph", At: e.cfg.Now().Unix()}
	err := e.journal.Update(ctx, ev, func(v *View) error {
		g, err := loadGoal(v, id)
		if err != nil || !graphOwner(g, person, agent, conversation) {
			return GraphUnavailable
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		current := graphFor(g)
		if expected == ^uint64(0) {
			return GraphStaleRevision
		}
		if current.Revision == expected+1 && reflect.DeepEqual(current.Steps, steps) {
			result = current
			return errNoChange
		}
		if current.Revision != expected {
			return GraphStaleRevision
		}
		if g.DefinitionPending {
			return GraphUnavailable
		}
		if err = validateSteps(g, steps); err != nil {
			return err
		}
		changed := len(g.Tasks) != len(steps)
		for _, s := range steps {
			old := goalTask(&g, s.ID)
			changed = changed || old == nil || !sameStepWork(*old, s)
		}
		if changed {
			if g.State == GoalDone || g.State == GoalStopped {
				return GraphUnavailable
			}
			g.Revision++
			if err = retireUndispatchedActions(v, &g); err != nil {
				return err
			}
		}
		byID := map[string]Step{}
		for _, s := range steps {
			byID[s.ID] = s
		}
		for _, old := range g.Tasks {
			s, exists := byID[old.ID]
			if (old.Attempt > 0 || old.Stage != TaskReady) && (!exists || !sameStepWork(old, s)) {
				return GraphStartedStep
			}
		}
		next := make([]Task, 0, len(steps))
		for _, s := range steps {
			task := s.task()
			if old := goalTask(&g, s.ID); old != nil && sameStepWork(*old, s) {
				task = *old
				task.Name = s.Name
				task.DependsOn = s.DependsOn
			}
			next = append(next, task)
		}
		g.Tasks = next
		g.Criteria = goalCriteria(g)
		if changed {
			g.Checks = nil
			g.Verification = Proof{}
			g.AssessedFailure = ""
			g.AttentionID = ""
			g.BlockedReason = ""
			g.State = GoalRunning
		}
		result = StepGraph{GoalID: id, Revision: expected + 1, Steps: steps}
		g.StepGraph = &result
		return put(v, "goal", g.ID, g)
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	if err != nil {
		return StepGraph{}, err
	}
	return result, nil
}

// Repair provenance is Core-owned state, not a manager-editable work field.
func sameStepWork(old Task, step Step) bool {
	proposed := step.task()
	proposed.Repairs = old.Repairs
	return sameTaskDefinition(old, proposed)
}
