// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/work/observation"
)

// ReportProgress records an observation from the current worker run, never a
// completion or a Core verdict. Sequence identifies a source event for retries;
// the existing ledger and card projection preserve it across rebuilds.
func (s *Service) ReportProgress(ctx context.Context, taskID, runID string, sequence uint64, summary string) error {
	if sequence == 0 || strings.TrimSpace(summary) == "" {
		return ErrInvalid
	}
	return s.append(ctx, Event{Key: fmt.Sprintf("worker-progress:%s:%d", runID, sequence), TaskID: taskID, RunID: runID, Kind: "worker_progress", Value: summary})
}

// ReportActivity records a safe activity class, separate from worker prose.
func (s *Service) ReportActivity(ctx context.Context, taskID, runID string, sequence uint64, activity string) error {
	if sequence == 0 {
		return ErrInvalid
	}
	return s.append(ctx, Event{Key: fmt.Sprintf("worker-activity:%s:%d", runID, sequence), TaskID: taskID, RunID: runID, Kind: "worker_activity", Value: activity})
}

// ReportDoing preserves only the defanged observation, not its source records.
func (s *Service) ReportDoing(ctx context.Context, taskID, runID string, sequence uint64, value observation.Snapshot) error {
	if sequence == 0 || value.Since.IsZero() {
		return ErrInvalid
	}
	value.Line = observation.Clean(value.Line)
	return s.append(ctx, Event{Key: fmt.Sprintf("worker-doing:%s:%d", runID, sequence), TaskID: taskID, RunID: runID, Kind: "worker_doing", Doing: &value})
}
