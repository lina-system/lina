// SPDX-License-Identifier: Apache-2.0
package boot

import "testing"

// Keep the installed runtime pointer stable while live owner views read it.
// Synthetic sessions use the same registry lock as real worker admission.
func addWorkerFixtureSessions(t *testing.T, r *conversationRuntime, sessions map[string]*workerSession) {
	t.Helper()
	b := r.workers
	b.mu.Lock()
	for id, session := range sessions {
		b.sessions[id] = session
	}
	b.mu.Unlock()
	t.Cleanup(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for id := range sessions {
			delete(b.sessions, id)
		}
	})
}
