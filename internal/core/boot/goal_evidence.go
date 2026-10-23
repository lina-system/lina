// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	goal "github.com/thisisjun786/lina/internal/core/work/goal"
)

func (d *codeGoalDriver) collectedCheck(path string, check goal.CriterionCheck) (*goal.CollectedEvidence, error) {
	if filepath.Dir(path) != filepath.Join(d.runtime.root, "work", "goal-evidence") {
		return nil, goal.ErrInvalid
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record codeCheckRecord
	if err = json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	if record.Output.Collected != nil {
		return record.Output.Collected, nil
	}
	// Existing installed checks retain their entire structured command output.
	// New citation/rubric/read connectors must return their actual source data.
	output, err := json.Marshal(record.Output)
	if err != nil {
		return nil, err
	}
	if check.Kind == goal.RunEvidence {
		return &goal.CollectedEvidence{Output: string(output)}, nil
	}
	if check.Kind == goal.ReadEvidence && check.Method == "deploy.read" {
		return &goal.CollectedEvidence{Record: output}, nil
	}
	return nil, goal.ErrInvalid
}

func (d *codeGoalDriver) RereadEvidence(ctx context.Context, a goal.Action, _ goal.CriterionEvidence) (goal.CriterionEvidence, error) {
	// Execute only the check. Merge, deploy and worker effects are never replayed.
	// qa.read reads the retained output; external records and probes are read live.
	observation, err := d.Execute(ctx, a)
	if err != nil {
		return goal.CriterionEvidence{}, err
	}
	if observation.Criterion == nil {
		return goal.CriterionEvidence{}, goal.ErrInvalid
	}
	return *observation.Criterion, nil
}
