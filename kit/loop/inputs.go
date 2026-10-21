// SPDX-License-Identifier: Apache-2.0
package loop

import (
	"sync"

	"github.com/thisisjun786/lina/kit/provider"
)

// InputQueue hands steering to one Run. Close must atomically end admission and
// return everything not drained; later producers must retain ownership of input.
type InputQueue interface {
	Drain() []provider.Item
	Close() []provider.Item
}

// Inputs is a caller-owned steering queue for one Run. After Close, the admission
// lane routes rejected pushes to the next turn instead of leaving them queued.
type Inputs struct {
	mu     sync.Mutex
	items  []provider.Item
	closed bool
}

// Push snapshots bytes when admission succeeds. False leaves ownership with the
// caller, which must route the item as follow-up input using the next turn's queue.
func (q *Inputs) Push(item provider.Item) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.items = append(q.items, append(provider.Item(nil), item...))
	return true
}
func (q *Inputs) Drain() []provider.Item {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.items
	q.items = nil
	return items
}

// Close atomically transfers remaining input to the caller and ends admission.
// It is safe to repeat, but only the first call can return pending input.
func (q *Inputs) Close() []provider.Item {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	items := q.items
	q.items = nil
	return items
}
