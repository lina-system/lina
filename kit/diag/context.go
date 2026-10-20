// SPDX-License-Identifier: Apache-2.0
package diag

import (
	"context"
	"time"
)

type commitDeadlineKey struct{}

// Terminal reports outcomes that must survive cancellation of the work that
// produced them. Pending and successful events keep the caller's lifetime.
func (r Result) Terminal() bool {
	return r == Failure || r == Unknown || r == Denied || r == Cancelled
}

// CommitContext gives a terminal record an independent five-second budget,
// preserving context values but not the caller's cancellation or deadline.
// Emit and writers both use it: nested calls retain the first commit deadline
// so crossing the writer boundary never renews an expired budget. Call cancel
// on every return path to release the timer. Other results use ctx unchanged.
func CommitContext(ctx context.Context, result Result) (context.Context, context.CancelFunc) {
	if !result.Terminal() {
		return ctx, func() {}
	}
	budget := 5 * time.Second
	if deadline, ok := ctx.Value(commitDeadlineKey{}).(time.Time); ok {
		budget = min(budget, time.Until(deadline))
	}
	commit, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	deadline, _ := commit.Deadline()
	return context.WithValue(commit, commitDeadlineKey{}, deadline), cancel
}
