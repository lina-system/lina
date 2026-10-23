// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
)

// liveWorkerSteps is the keep set of the goal engine: the steps in progress or
// under pull-request operation. A missing engine keeps no session, so the sweep
// ends what no step can justify.
func (r *conversationRuntime) liveWorkerSteps(ctx context.Context) (map[string]bool, error) {
	if r.liveWorkers == nil {
		return nil, nil
	}
	if r.dispatchMu != nil {
		r.dispatchMu.RLock()
	}
	engine := r.workEngine
	if r.dispatchMu != nil {
		r.dispatchMu.RUnlock()
	}
	if engine == nil || engine.engine == nil {
		return map[string]bool{}, nil
	}
	return engine.engine.LiveSessions(ctx)
}
