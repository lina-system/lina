// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// Display reads need task states and graph topology, not execution briefs,
// repository settings or result receipts. Keep the narrow decoding here so
// both current goals and historical revisions use the same projection inputs.
// The returned Goal is only for display; it must never drive execution.
func decodeProgressGoal(raw []byte) (goal.Goal, error) {
	var display struct {
		ID          string         `json:"id"`
		Person      string         `json:"person"`
		Agent       string         `json:"agent"`
		Turn        string         `json:"turn"`
		CreatedAt   int64          `json:"created_at"`
		State       goal.GoalState `json:"state"`
		Rank        int            `json:"rank"`
		Paused      bool           `json:"paused"`
		AttentionID string         `json:"attention_id"`
		QueuePlace  int            `json:"queue_place"`
		QueueReason string         `json:"queue_reason"`
		Tasks       []struct {
			ID      string         `json:"id"`
			Stage   goal.TaskStage `json:"stage"`
			Session string         `json:"session"`
		} `json:"tasks"`
		StepGraph *struct {
			GoalID   string `json:"goal_id"`
			Revision uint64 `json:"revision"`
			Steps    []struct {
				ID        string   `json:"id"`
				Name      string   `json:"name"`
				DependsOn []string `json:"depends_on"`
			} `json:"steps"`
		} `json:"step_graph"`
	}
	if err := json.Unmarshal(raw, &display); err != nil {
		return goal.Goal{}, err
	}
	g := goal.Goal{ID: display.ID, Person: display.Person, Agent: display.Agent, Turn: display.Turn, CreatedAt: display.CreatedAt, State: display.State, AttentionID: display.AttentionID, Rank: display.Rank, Paused: display.Paused, QueuePlace: display.QueuePlace, QueueReason: display.QueueReason}
	if display.Tasks != nil {
		g.Tasks = make([]goal.Task, len(display.Tasks))
		for i, task := range display.Tasks {
			g.Tasks[i] = goal.Task{ID: task.ID, Stage: task.Stage, Session: task.Session}
		}
	}
	if graph := display.StepGraph; graph != nil {
		g.StepGraph = &goal.StepGraph{GoalID: graph.GoalID, Revision: graph.Revision}
		if graph.Steps != nil {
			g.StepGraph.Steps = make([]goal.Step, len(graph.Steps))
			for i, step := range graph.Steps {
				g.StepGraph.Steps[i] = goal.Step{ID: step.ID, Name: step.Name, DependsOn: step.DependsOn}
			}
		}
	}
	return g, nil
}
