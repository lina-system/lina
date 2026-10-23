// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"strings"

	"github.com/thisisjun786/lina/internal/core/runs"
)

type StepKind string

const (
	TaskStep       StepKind = "task"
	BackgroundStep StepKind = "background"
	WaitStep       StepKind = "wait"
)

// StepWait names a date or a canonical record revision to wait beyond.
// Record reads are hints until the owning Core observes a different revision.
type StepWait struct {
	At       int64  `json:"at,omitempty"`
	RecordID string `json:"record_id,omitempty"`
	Revision string `json:"revision,omitempty"`
	Deadline int64  `json:"deadline,omitempty"`
}

func stepKind(k StepKind) StepKind {
	if k == "" {
		return TaskStep
	}
	return k
}

func validStepWork(t Task) bool {
	if t.ID == "" || strings.TrimSpace(t.Done) == "" || t.Repository == "" {
		return false
	}
	switch stepKind(t.Kind) {
	case TaskStep:
		return t.Background == nil && t.Wait == nil && t.Base != "" && t.Area != "" && t.Worker != "" && validResources(t.Resources)
	case BackgroundStep:
		b := t.Background
		if b == nil || t.Wait != nil || !validResources(t.Resources) || t.Worker == "" {
			return false
		}
		if b.Goal == "" || b.Scope == "" || b.NotTouch == "" || b.Done == "" || b.Result == "" || len(b.Checks) == 0 {
			return false
		}
		for _, c := range b.Checks {
			if c.Name == "" || c.Kind != runs.Claimed && c.Kind != runs.ToolCalled && c.Kind != runs.CitationsResolve {
				return false
			}
		}
		return true
	case WaitStep:
		w := t.Wait
		return t.Background == nil && t.Area == "" && t.Base == "" && t.Worker == "" && t.Resources == (Resources{}) && w != nil && ((w.At > 0 && w.RecordID == "" && w.Revision == "" && w.Deadline == 0) || (w.At == 0 && w.RecordID != "" && w.Revision != "" && w.Deadline > 0))
	}
	return false
}

func stepLanded(t Task) bool {
	if stepKind(t.Kind) == TaskStep {
		return t.Merge.proves(t.Merge.Revision)
	}
	return t.Stage == TaskDone && t.Result.proves(t.Result.Revision)
}

type FrontierStep struct {
	GoalID string   `json:"goal_id"`
	StepID string   `json:"step_id"`
	Kind   StepKind `json:"kind"`
}
