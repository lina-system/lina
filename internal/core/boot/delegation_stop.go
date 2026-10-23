// SPDX-License-Identifier: Apache-2.0
package boot

import "context"

func (r *conversationRuntime) stopScheduled(ctx context.Context, person string, conversation *string, reference string) (int, error) {
	if r.work == nil {
		return 0, nil
	}
	scope := ""
	if conversation != nil {
		scope = *conversation
	}
	if r.delegation != nil {
		return r.delegation.reactor.Stop(ctx, person, scope, reference)
	}
	// Optional worker unavailability must not make saved queued work unstoppable.
	return r.work.StopScheduled(ctx, person, scope, reference)
}
