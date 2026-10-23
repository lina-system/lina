// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"fmt"
	"strings"
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
